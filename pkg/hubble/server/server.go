// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Hubble

package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	observerpb "github.com/cilium/cilium/api/v1/observer"
	peerpb "github.com/cilium/cilium/api/v1/peer"
	recorderpb "github.com/cilium/cilium/api/v1/recorder"
	"github.com/cilium/cilium/pkg/hubble/server/serveroption"
)

var (
	errNoListener        = errors.New("no listener configured")
	errNoServerTLSConfig = errors.New("no server TLS config is set")
)

// Server is hubble's gRPC server.
type Server struct {
	log  *slog.Logger
	srv  *grpc.Server
	opts serveroption.Options
}

// NewServer creates a new hubble gRPC server.
func NewServer(log *slog.Logger, options ...serveroption.Option) (*Server, error) {
	opts := serveroption.Options{}
	for _, opt := range options {
		if err := opt(&opts); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}
	if opts.Listener == nil {
		return nil, errNoListener
	}
	if opts.ServerTLSConfig == nil && !opts.Insecure {
		return nil, errNoServerTLSConfig
	}

	s := &Server{log: log, opts: opts}
	s.initGRPCServer()
	return s, nil
}

func (s *Server) newGRPCServer() *grpc.Server {
	var opts []grpc.ServerOption
	if len(s.opts.GRPCUnaryInterceptors) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(s.opts.GRPCUnaryInterceptors...))
	}
	if len(s.opts.GRPCStreamInterceptors) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(s.opts.GRPCStreamInterceptors...))
	}
	if s.opts.ServerTLSConfig != nil {
		// NOTE: gosec is unable to resolve the constant and warns about "TLS
		// MinVersion too low".
		tlsConfig := s.opts.ServerTLSConfig.ServerConfig(&tls.Config{ //nolint:gosec
			MinVersion: serveroption.MinTLSVersion,
		})
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}
	return grpc.NewServer(opts...)
}

func (s *Server) initGRPCServer() {
	srv := s.newGRPCServer()
	if s.opts.HealthService != nil {
		healthpb.RegisterHealthServer(srv, s.opts.HealthService)
	}
	if s.opts.ObserverService != nil {
		observerpb.RegisterObserverServer(srv, s.opts.ObserverService)
	}
	if s.opts.PeerService != nil {
		peerpb.RegisterPeerServer(srv, s.opts.PeerService)
	}
	if s.opts.RecorderService != nil {
		recorderpb.RegisterRecorderServer(srv, s.opts.RecorderService)
	}
	reflection.Register(srv)
	if s.opts.GRPCMetrics != nil {
		s.opts.GRPCMetrics.InitializeMetrics(srv)
	}
	s.srv = srv
}

// Serve starts the hubble server and accepts new connections on the configured
// listener. Stop should be called to stop the server.
func (s *Server) Serve() error {
	return s.srv.Serve(s.opts.Listener)
}

// Stop stops the hubble server.
func (s *Server) Stop() {
	s.srv.Stop()
}
