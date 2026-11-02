package protocols

import (
	"context"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// GRPCProtocol is the in-core gRPC protocol. Unary and streaming calls ship
// in the first-party out-of-process plugin plugins/grpc, which registers the same
// type id and replaces this stub when the project loads it. Core stays free
// of a grpc-go dependency. See docs/proposals/grpc.md.
type GRPCProtocol struct{}

func (GRPCProtocol) Type() string { return "grpc" }

func (GRPCProtocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	return &spi.Request{Config: raw}, nil
}

func (GRPCProtocol) Execute(_ context.Context, _ *spi.ProtocolContext, _ *spi.Request) (*spi.Response, error) {
	return nil, verr.New(verr.CodePlugin,
		"gRPC calls are provided by the first-party plugin plugins/grpc; core does not link grpc-go (see docs/extending-protocols.md)").
		WithDetail("protocol", "grpc").
		WithDetail("status", "stub")
}

// RegisterGRPC registers the gRPC stub protocol.
func RegisterGRPC(r *registry.Registry) {
	r.RegisterProtocol(GRPCProtocol{})
}
