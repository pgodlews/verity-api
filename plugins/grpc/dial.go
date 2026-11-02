package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/pgodlews/verity-api/core/verr"
)

func transportCreds(dir string, allowExternal, plaintext bool, tlsCfg map[string]any) (credentials.TransportCredentials, error) {
	if plaintext {
		return insecure.NewCredentials(), nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if tlsCfg != nil {
		if v, ok := tlsCfg["insecure"].(bool); ok {
			cfg.InsecureSkipVerify = v
		}
		if s, ok := tlsCfg["serverName"].(string); ok {
			cfg.ServerName = s
		}
		if path, ok := tlsCfg["caFile"].(string); ok && path != "" {
			pemBytes, err := readProjectFile(dir, allowExternal, path, "caFile")
			if err != nil {
				return nil, err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pemBytes) {
				return nil, verr.Newf(verr.CodeConfig, "grpc tls.caFile %q has no certificates", path)
			}
			cfg.RootCAs = pool
		}
	}
	return credentials.NewTLS(cfg), nil
}

// dial opens a client and waits until the connection is ready or ctx ends.
// A failure here is a transport error; it is not a gRPC status response.
func dial(ctx context.Context, target string, creds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(creds),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxPayloadBytes)),
	)
	if err != nil {
		return nil, verr.Wrap(verr.CodeTransport, err, "dial grpc")
	}
	if err := waitReady(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func waitReady(ctx context.Context, conn *grpc.ClientConn) error {
	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if state == connectivity.Shutdown {
			return verr.New(verr.CodeTransport, "grpc connection shut down")
		}
		if !conn.WaitForStateChange(ctx, state) {
			if ctx.Err() != nil {
				return verr.Wrap(verr.CodeTransport, ctx.Err(), "dial grpc target")
			}
			return verr.New(verr.CodeTransport, "dial grpc target")
		}
	}
}
