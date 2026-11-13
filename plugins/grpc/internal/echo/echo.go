// Package echo is a tiny in-process gRPC service used by plugin tests.
// Descriptors are built in code so the module does not need protoc.
package echo

import (
	"context"
	"io"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	v1alpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	// Service is the fully qualified service name.
	Service = "verity.testpb.Echo"
	// MethodEcho echoes the request and selected metadata.
	MethodEcho = Service + "/Echo"
	// MethodFail returns a NotFound status.
	MethodFail = Service + "/Fail"
	// MethodWait blocks until the client context ends.
	MethodWait = Service + "/Wait"
)

// Reflection selects which reflection services a test server registers.
type Reflection int

const (
	// ReflectionBoth registers reflection v1 and v1alpha.
	ReflectionBoth Reflection = iota
	// ReflectionOff registers no reflection service.
	ReflectionOff
	// ReflectionV1Alpha registers only reflection v1alpha.
	ReflectionV1Alpha
)

var (
	reqDesc  protoreflect.MessageDescriptor
	repDesc  protoreflect.MessageDescriptor
	setBytes []byte
)

func init() {
	fd, b, err := buildFile()
	if err != nil {
		panic(err)
	}
	setBytes = b
	reqDesc = fd.Messages().ByName("EchoRequest")
	repDesc = fd.Messages().ByName("EchoReply")
	if reqDesc == nil || repDesc == nil {
		panic("echo descriptors missing")
	}
	if _, err := protoregistry.GlobalFiles.FindFileByPath(fd.Path()); err != nil {
		if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
			panic(err)
		}
	}
}

// DescriptorSet returns a FileDescriptorSet for the echo service.
func DescriptorSet() []byte {
	return append([]byte(nil), setBytes...)
}

// NewServer returns a server for the echo service. lis is owned by the caller.
func NewServer(mode Reflection) *grpc.Server {
	s := grpc.NewServer()
	s.RegisterService(&echoServiceDesc, struct{}{})
	switch mode {
	case ReflectionOff:
	case ReflectionV1Alpha:
		v1alpha.RegisterServerReflectionServer(s, reflection.NewServer(reflection.ServerOptions{Services: s}))
	default:
		reflection.Register(s)
	}
	return s
}

var echoServiceDesc = grpc.ServiceDesc{
	ServiceName: Service,
	HandlerType: (*echoSvc)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Echo", Handler: unary(doEcho)},
		{MethodName: "Fail", Handler: unary(doFail)},
		{MethodName: "Wait", Handler: unary(doWait)},
	},
	Streams: []grpc.StreamDesc{
		{StreamName: "ServerStream", Handler: serverStream, ServerStreams: true},
		{StreamName: "ClientStream", Handler: clientStream, ClientStreams: true},
		{StreamName: "Stream", Handler: bidiStream, ClientStreams: true, ServerStreams: true},
	},
	Metadata: "echo.proto",
}

type echoSvc interface{}

func unary(fn func(context.Context, *dynamicpb.Message) (*dynamicpb.Message, error)) grpc.MethodHandler {
	return func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := dynamicpb.NewMessage(reqDesc)
		if err := dec(in); err != nil {
			return nil, err
		}
		return fn(ctx, in)
	}
}

func doEcho(ctx context.Context, in *dynamicpb.Message) (*dynamicpb.Message, error) {
	_ = grpc.SendHeader(ctx, metadata.Pairs("x-echo", "ok"))
	grpc.SetTrailer(ctx, metadata.Pairs("x-trailer", "yes"))
	return echoReply(ctx, in), nil
}

func echoReply(ctx context.Context, in *dynamicpb.Message) *dynamicpb.Message {
	out := dynamicpb.NewMessage(repDesc)
	if s := fieldString(in, "message"); s != "" {
		setString(out, "message", s)
	}
	if n := fieldInt(in, "n"); n != 0 {
		setInt(out, "n", n)
	}
	if tok := firstMeta(ctx, "x-token"); tok != "" {
		setString(out, "seen_metadata", tok)
	} else if authz := firstMeta(ctx, "authorization"); authz != "" {
		setString(out, "seen_metadata", authz)
	}
	return out
}

func doFail(ctx context.Context, _ *dynamicpb.Message) (*dynamicpb.Message, error) {
	_ = grpc.SendHeader(ctx, metadata.Pairs("x-from-server", "fail"))
	return nil, status.Error(codes.NotFound, "missing widget")
}

func doWait(ctx context.Context, _ *dynamicpb.Message) (*dynamicpb.Message, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func firstMeta(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vs := md.Get(key)
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

func fieldString(m protoreflect.Message, name protoreflect.Name) string {
	fd := m.Descriptor().Fields().ByName(name)
	if fd == nil || !m.Has(fd) {
		return ""
	}
	return m.Get(fd).String()
}

func fieldInt(m protoreflect.Message, name protoreflect.Name) int32 {
	fd := m.Descriptor().Fields().ByName(name)
	if fd == nil || !m.Has(fd) {
		return 0
	}
	return int32(m.Get(fd).Int())
}

func setString(m protoreflect.Message, name protoreflect.Name, v string) {
	fd := m.Descriptor().Fields().ByName(name)
	m.Set(fd, protoreflect.ValueOfString(v))
}

func setInt(m protoreflect.Message, name protoreflect.Name, v int32) {
	fd := m.Descriptor().Fields().ByName(name)
	m.Set(fd, protoreflect.ValueOfInt32(v))
}

func buildFile() (protoreflect.FileDescriptor, []byte, error) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("echo.proto"),
		Package: proto.String("verity.testpb"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			msg("EchoRequest",
				field("message", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("n", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
			),
			msg("EchoReply",
				field("message", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				field("n", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
				field("seen_metadata", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			),
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("Echo"),
			Method: []*descriptorpb.MethodDescriptorProto{
				method("Echo", false, false),
				method("Fail", false, false),
				method("Wait", false, false),
				method("ServerStream", false, true),
				method("ClientStream", true, false),
				method("Stream", true, true),
			},
		}},
	}
	fd, err := protodesc.NewFile(file, nil)
	if err != nil {
		return nil, nil, err
	}
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(fd),
	}}
	b, err := proto.Marshal(set)
	if err != nil {
		return nil, nil, err
	}
	return fd, b, nil
}

func msg(name string, fields ...*descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{Name: proto.String(name), Field: fields}
}

func field(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:   proto.String(name),
		Number: proto.Int32(num),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:   typ.Enum(),
	}
}

func method(name string, client, server bool) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:            proto.String(name),
		InputType:       proto.String(".verity.testpb.EchoRequest"),
		OutputType:      proto.String(".verity.testpb.EchoReply"),
		ClientStreaming: proto.Bool(client),
		ServerStreaming: proto.Bool(server),
	}
}

func streamMetadata(stream grpc.ServerStream) {
	_ = stream.SendHeader(metadata.Pairs("x-echo", "ok"))
	stream.SetTrailer(metadata.Pairs("x-trailer", "yes"))
}

func serverStream(_ any, stream grpc.ServerStream) error {
	in := dynamicpb.NewMessage(reqDesc)
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	streamMetadata(stream)
	n := fieldInt(in, "n")
	if n == 0 {
		n = 3
	}
	for i := int32(1); i <= n; i++ {
		out := echoReply(stream.Context(), in)
		setInt(out, "n", i)
		if err := stream.SendMsg(out); err != nil {
			return err
		}
		switch fieldString(in, "message") {
		case "wait":
			<-stream.Context().Done()
			return status.FromContextError(stream.Context().Err()).Err()
		case "fail":
			if i == 2 {
				return status.Error(codes.NotFound, "stream stopped")
			}
		}
	}
	return nil
}

func clientStream(_ any, stream grpc.ServerStream) error {
	streamMetadata(stream)
	var values []string
	for {
		in := dynamicpb.NewMessage(reqDesc)
		err := stream.RecvMsg(in)
		if err == io.EOF {
			out := dynamicpb.NewMessage(repDesc)
			setString(out, "message", strings.Join(values, ","))
			setInt(out, "n", int32(len(values)))
			return stream.SendMsg(out)
		}
		if err != nil {
			return err
		}
		if fieldString(in, "message") == "fail" {
			return status.Error(codes.NotFound, "stream stopped")
		}
		values = append(values, fieldString(in, "message"))
	}
}

func bidiStream(_ any, stream grpc.ServerStream) error {
	streamMetadata(stream)
	for {
		in := dynamicpb.NewMessage(reqDesc)
		err := stream.RecvMsg(in)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.SendMsg(echoReply(stream.Context(), in)); err != nil {
			return err
		}
		if fieldString(in, "message") == "fail" {
			return status.Error(codes.NotFound, "stream stopped")
		}
	}
}
