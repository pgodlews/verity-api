//lint:file-ignore SA1019 reflectAlpha deliberately uses the deprecated reflection v1alpha API as the fallback for servers without reflection v1.

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	reflectv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	reflectalpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// Well-known types register themselves so a descriptor set can import
	// them even when the payload does not repeat those files.
	_ "google.golang.org/protobuf/types/known/anypb"
	_ "google.golang.org/protobuf/types/known/durationpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/structpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	_ "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/pgodlews/verity-api/core/verr"
)

// maxPayloadBytes caps descriptor files and inbound gRPC messages.
const maxPayloadBytes = 32 << 20

func filesFromDescriptorSet(b []byte) (*protoregistry.Files, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(b, &set); err != nil {
		return nil, verr.New(verr.CodeConfig, "read grpc descriptor set: "+err.Error())
	}
	if len(set.File) == 0 {
		return nil, verr.New(verr.CodeConfig, "grpc descriptor set contains no files")
	}
	return filesFromDescriptorProtos(set.File)
}

func filesFromDescriptorProtos(fds []*descriptorpb.FileDescriptorProto) (*protoregistry.Files, error) {
	byName := map[string]*descriptorpb.FileDescriptorProto{}
	var order []string
	for _, fd := range fds {
		if fd == nil || fd.GetName() == "" {
			return nil, verr.New(verr.CodeConfig, "grpc file descriptor is missing a name")
		}
		if _, ok := byName[fd.GetName()]; !ok {
			order = append(order, fd.GetName())
		}
		byName[fd.GetName()] = fd
	}
	files := &protoregistry.Files{}
	visiting := map[string]bool{}
	var add func(name string) error
	add = func(name string) error {
		if _, err := files.FindFileByPath(name); err == nil {
			return nil
		}
		fd, ok := byName[name]
		if !ok {
			gf, err := protoregistry.GlobalFiles.FindFileByPath(name)
			if err != nil {
				return verr.Newf(verr.CodeConfig, "grpc descriptor import %q is not available", name)
			}
			return registerGlobal(files, gf)
		}
		if visiting[name] {
			return verr.Newf(verr.CodeConfig, "grpc descriptor import cycle at %q", name)
		}
		visiting[name] = true
		for _, dep := range fd.GetDependency() {
			if err := add(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		file, err := protodesc.NewFile(fd, resolver{files: files})
		if err != nil {
			return verr.New(verr.CodeConfig, "build grpc descriptor: "+err.Error())
		}
		if err := files.RegisterFile(file); err != nil {
			return verr.New(verr.CodeConfig, "register grpc descriptor: "+err.Error())
		}
		return nil
	}
	for _, name := range order {
		if err := add(name); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func registerGlobal(files *protoregistry.Files, gf protoreflect.FileDescriptor) error {
	if gf == nil {
		return verr.New(verr.CodeConfig, "grpc descriptor import is missing")
	}
	if _, err := files.FindFileByPath(gf.Path()); err == nil {
		return nil
	}
	imps := gf.Imports()
	for i := 0; i < imps.Len(); i++ {
		if err := registerGlobal(files, imps.Get(i)); err != nil {
			return err
		}
	}
	if err := files.RegisterFile(gf); err != nil {
		return verr.New(verr.CodeConfig, "register grpc descriptor import: "+err.Error())
	}
	return nil
}

// resolver looks in the set being built, then in the process global registry.
type resolver struct {
	files *protoregistry.Files
}

func (r resolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := r.files.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return protoregistry.GlobalFiles.FindFileByPath(path)
}

func (r resolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := r.files.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return protoregistry.GlobalFiles.FindDescriptorByName(name)
}

func findMethod(files *protoregistry.Files, service, method string) (protoreflect.MethodDescriptor, error) {
	desc, err := files.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, verr.Newf(verr.CodeConfig, "grpc service %q not found", service)
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, verr.Newf(verr.CodeConfig, "grpc symbol %q is not a service", service)
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil, verr.Newf(verr.CodeConfig, "grpc method %q not found on %s", method, service)
	}
	return md, nil
}

// reflectFiles loads descriptors for service (or service.method) using
// reflection v1, then v1alpha when v1 is not implemented.
func reflectFiles(ctx context.Context, conn *grpc.ClientConn, service, method string) (*protoregistry.Files, error) {
	protos, err := reflectSymbol(ctx, conn, service)
	if err != nil {
		if ve, ok := verr.As(err); ok {
			return nil, ve
		}
		if status.Code(err) == codes.NotFound {
			protos, err = reflectSymbol(ctx, conn, service+"."+method)
		}
	}
	if err != nil {
		if ve, ok := verr.As(err); ok {
			return nil, ve
		}
		code := status.Code(err)
		if code == codes.Unavailable || code == codes.DeadlineExceeded || code == codes.Canceled {
			return nil, verr.Wrap(verr.CodeTransport, err, "grpc server reflection")
		}
		return nil, verr.Newf(verr.CodeConfig, "grpc server reflection failed: %v", err)
	}
	return filesFromDescriptorProtos(protos)
}

func reflectSymbol(ctx context.Context, conn *grpc.ClientConn, symbol string) ([]*descriptorpb.FileDescriptorProto, error) {
	protos, err := reflectV1(ctx, conn, symbol)
	if err != nil && status.Code(err) == codes.Unimplemented {
		return reflectAlpha(ctx, conn, symbol)
	}
	return protos, err
}

func reflectV1(ctx context.Context, conn *grpc.ClientConn, symbol string) ([]*descriptorpb.FileDescriptorProto, error) {
	stream, err := reflectv1.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.CloseSend()
	if err := stream.Send(&reflectv1.ServerReflectionRequest{
		MessageRequest: &reflectv1.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: symbol,
		},
	}); err != nil {
		return nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	if er := resp.GetErrorResponse(); er != nil {
		return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
	}
	fd := resp.GetFileDescriptorResponse()
	if fd == nil || len(fd.GetFileDescriptorProto()) == 0 {
		return nil, status.Error(codes.NotFound, "reflection returned no file descriptors")
	}
	return unmarshalDescriptors(fd.GetFileDescriptorProto())
}

func reflectAlpha(ctx context.Context, conn *grpc.ClientConn, symbol string) ([]*descriptorpb.FileDescriptorProto, error) {
	stream, err := reflectalpha.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.CloseSend()
	if err := stream.Send(&reflectalpha.ServerReflectionRequest{
		MessageRequest: &reflectalpha.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: symbol,
		},
	}); err != nil {
		return nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	if er := resp.GetErrorResponse(); er != nil {
		return nil, status.Error(codes.Code(er.GetErrorCode()), er.GetErrorMessage())
	}
	fd := resp.GetFileDescriptorResponse()
	if fd == nil || len(fd.GetFileDescriptorProto()) == 0 {
		return nil, status.Error(codes.NotFound, "reflection returned no file descriptors")
	}
	return unmarshalDescriptors(fd.GetFileDescriptorProto())
}

func unmarshalDescriptors(raw [][]byte) ([]*descriptorpb.FileDescriptorProto, error) {
	out := make([]*descriptorpb.FileDescriptorProto, 0, len(raw))
	for _, b := range raw {
		fd := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(b, fd); err != nil {
			return nil, verr.New(verr.CodeConfig, "parse reflected descriptor: "+err.Error())
		}
		out = append(out, fd)
	}
	return out, nil
}

// readProjectFile reads path relative to dir. When dir is set and external
// files are not allowed, the file must stay inside dir after symlink
// resolution — the same confinement REST uploads use.
func readProjectFile(dir string, allowExternal bool, userPath, what string) ([]byte, error) {
	if userPath == "" || strings.ContainsAny(userPath, "\r\n") {
		return nil, verr.Newf(verr.CodeConfig, "grpc %s must be a non-empty path", what)
	}
	resolved := userPath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(dir, resolved)
	}
	if dir != "" && !allowExternal {
		if err := insideProject(dir, resolved); err != nil {
			return nil, verr.Newf(verr.CodeConfig, "grpc %s %q is outside the project directory", what, userPath)
		}
	}
	f, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, verr.Newf(verr.CodeConfig, "grpc %s %q not found", what, userPath)
		}
		return nil, verr.Newf(verr.CodeConfig, "open grpc %s %q", what, userPath)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, verr.Newf(verr.CodeConfig, "stat grpc %s %q", what, userPath)
	}
	if info.IsDir() {
		return nil, verr.Newf(verr.CodeConfig, "grpc %s %q is a directory", what, userPath)
	}
	if info.Size() > maxPayloadBytes {
		return nil, verr.Newf(verr.CodeConfig, "grpc %s %q is larger than %d bytes", what, userPath, maxPayloadBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPayloadBytes+1))
	if err != nil {
		return nil, verr.Newf(verr.CodeConfig, "read grpc %s %q", what, userPath)
	}
	if int64(len(b)) > maxPayloadBytes {
		return nil, verr.Newf(verr.CodeConfig, "grpc %s %q is larger than %d bytes", what, userPath, maxPayloadBytes)
	}
	return b, nil
}

func insideProject(dir, path string) error {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return os.ErrPermission
	}
	return nil
}
