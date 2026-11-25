/*
Copyright 2026 Neotera.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package httpsse is the PROVISIONAL I-4 transport: HTTP/JSON with Server-Sent Events, built
// on the Go standard library only. It stands in until the I-4 transport is agreed (OPN-04).
//
//	GET /i4/v1alpha1                                  capabilities (JSON)
//	GET /i4/v1alpha1/watch?prefix=&since=&epoch=      subscription (text/event-stream)
//
// See the Event constants for the stream. TLS is required unless Options.Insecure is set
// explicitly; the security mechanism itself is open (OPN-11).
package httpsse

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"k8s.io/klog/v2"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
)

// Name is the transport name.
const Name = "httpsse"

const (
	// DefaultHeartbeatInterval is the HEARTBEAT interval on a quiet stream.
	DefaultHeartbeatInterval = 15 * time.Second
	// DefaultWriteTimeout bounds one write to a subscriber. A subscriber that does not read
	// for that long is disconnected.
	DefaultWriteTimeout = 30 * time.Second

	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 5 * time.Second
)

// Options configure the transport.
type Options struct {
	// Address is the listen address used by Run, for example ":8443".
	Address string
	// TLSConfig serves HTTPS. It must hold a server certificate. Required unless Insecure.
	TLSConfig *tls.Config
	// Insecure serves plain HTTP. For development playgrounds only.
	Insecure bool
	// HeartbeatInterval is the HEARTBEAT interval. Zero uses DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration
	// BufferSize is the event buffer per subscriber. Zero uses store.DefaultBufferSize.
	BufferSize int
	// WriteTimeout bounds one write to a subscriber. Zero uses DefaultWriteTimeout.
	WriteTimeout time.Duration
}

// Server is the httpsse Publisher.
type Server struct {
	opts    Options
	store   *store.Store
	info    transport.Info
	handler http.Handler
}

var _ transport.Publisher = (*Server)(nil)

// Factory returns a transport.Factory that creates a Server with opts.
func Factory(opts Options) transport.Factory {
	return func(deps transport.Deps) (transport.Publisher, error) {
		return New(opts, deps)
	}
}

// New creates a Server. It does not listen yet.
func New(opts Options, deps transport.Deps) (*Server, error) {
	if deps.Store == nil {
		return nil, errors.New("httpsse: store is not set")
	}
	switch {
	case opts.TLSConfig == nil && !opts.Insecure:
		return nil, errors.New("httpsse: TLS is required; plain HTTP needs the explicit insecure option")
	case opts.TLSConfig != nil && opts.Insecure:
		return nil, errors.New("httpsse: TLS and the insecure option are mutually exclusive")
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = store.DefaultBufferSize
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}

	s := &Server{opts: opts, store: deps.Store, info: deps.Info}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+BasePath, s.serveCapabilities)
	mux.HandleFunc("GET "+WatchPath, s.serveWatch)
	s.handler = mux
	return s, nil
}

// Name returns the transport name.
func (s *Server) Name() string {
	return Name
}

// Handler returns the HTTP handler of the I-4 API.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run listens on Options.Address and serves until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	if s.opts.Address == "" {
		return errors.New("httpsse: listen address is not set")
	}
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return fmt.Errorf("httpsse: listen on %s: %w", s.opts.Address, err)
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx is done, then closes open streams and stops. It closes ln.
// Cancelling ctx is a normal stop and returns nil.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		// Request contexts derive from ctx, so open streams end when ctx is done.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	// Decided once: srv.TLSConfig must not be read while Serve configures it.
	useTLS := s.opts.TLSConfig != nil
	if useTLS {
		srv.TLSConfig = s.opts.TLSConfig.Clone()
	}

	errCh := make(chan error, 1)
	go func() {
		if useTLS {
			errCh <- srv.ServeTLS(ln, "", "")
		} else {
			errCh <- srv.Serve(ln)
		}
	}()
	klog.Infof("dtpublisher: %s transport serving on %s (tls=%t)", Name, ln.Addr(), useTLS)

	select {
	case err := <-errCh:
		return fmt.Errorf("httpsse: serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		klog.Warningf("dtpublisher: %s transport shutdown: %v", Name, err)
		_ = srv.Close()
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("httpsse: serve: %w", err)
	}
	return nil
}

func (s *Server) operations() transport.Operations {
	return transport.Operations{Watch: true}
}

func (s *Server) serveCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, transport.NewCapabilities(Name, s.store, s.info, s.operations()))
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, format string, args ...interface{}) {
	writeJSON(w, status, errorBody{Error: fmt.Sprintf(format, args...)})
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	data, err := json.Marshal(body)
	if err != nil {
		klog.Errorf("dtpublisher: encode response: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(data, '\n')); err != nil {
		klog.V(2).Infof("dtpublisher: write response: %v", err)
	}
}
