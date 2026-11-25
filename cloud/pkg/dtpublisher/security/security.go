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

// Package security sets up the transport security of I-4 and establishes consumer identity.
//
// The security mechanism is an open decision (brief section 5, item 4; OPN-11), to be defined
// with WP4 (Cloud SASE Library, CID 30) against REQ-ORP-0040 ("secure handshake protocol and
// mutual authentication", "encrypted transport (e.g., TLS)"). The PROVISIONAL mechanism is
// TLS 1.2 or later from the Go standard library, with optional or required client
// certificates verified against a configured CA (mutual TLS). A consumer's identity is the
// common name of its verified client certificate. What a consumer may read is decided
// separately, by package access.
package security

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
)

// MinTLSVersion is the lowest TLS version accepted, on both sides.
const MinTLSVersion = tls.VersionTLS12

// ClientAuth is how the server treats client certificates.
type ClientAuth string

const (
	// ClientAuthNone asks for no client certificate; every consumer is anonymous.
	ClientAuthNone ClientAuth = "none"
	// ClientAuthOptional verifies a client certificate if one is presented.
	ClientAuthOptional ClientAuth = "optional"
	// ClientAuthRequire requires a client certificate signed by the client CA (mutual TLS).
	ClientAuthRequire ClientAuth = "require"
)

// ServerConfig is the TLS configuration of the publisher.
type ServerConfig struct {
	// CertFile and KeyFile are the PEM server certificate (with chain) and key.
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
	// ClientCAFile is the PEM bundle client certificates are verified against. Required
	// unless ClientAuth is none, and not allowed then.
	ClientCAFile string `json:"clientCAFile,omitempty"`
	// ClientAuth is the client certificate mode. Empty means ClientAuthNone.
	ClientAuth ClientAuth `json:"clientAuth,omitempty"`
}

// Validate checks the configuration without reading files.
func (c ServerConfig) Validate() error {
	if c.CertFile == "" || c.KeyFile == "" {
		return errors.New("security: server certificate and key files must be set")
	}
	switch c.mode() {
	case ClientAuthNone:
		if c.ClientCAFile != "" {
			return fmt.Errorf("security: a client CA is set but client auth is %q; set %q or %q", ClientAuthNone, ClientAuthOptional, ClientAuthRequire)
		}
	case ClientAuthOptional, ClientAuthRequire:
		if c.ClientCAFile == "" {
			return fmt.Errorf("security: client auth %q needs a client CA file", c.mode())
		}
	default:
		return fmt.Errorf("security: unknown client auth %q, valid: %q, %q, %q", c.ClientAuth, ClientAuthNone, ClientAuthOptional, ClientAuthRequire)
	}
	return nil
}

func (c ServerConfig) mode() ClientAuth {
	if c.ClientAuth == "" {
		return ClientAuthNone
	}
	return c.ClientAuth
}

// TLSConfig validates c, reads its files and returns the server TLS configuration. The
// files are read once; a rotated certificate takes effect after a restart.
func (c ServerConfig) TLSConfig() (*tls.Config, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("security: load server certificate: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   MinTLSVersion,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.NoClientCert,
	}
	if c.mode() == ClientAuthNone {
		return cfg, nil
	}
	if cfg.ClientCAs, err = loadPool(c.ClientCAFile); err != nil {
		return nil, fmt.Errorf("security: client CA: %w", err)
	}
	if c.mode() == ClientAuthRequire {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

// ClientConfig is the TLS configuration of a consumer, for example "dtpublisher ctl".
type ClientConfig struct {
	// CAFile is the PEM bundle the server certificate is verified against. Empty uses the
	// system roots.
	CAFile string
	// CertFile and KeyFile are the client certificate and key, for mutual TLS. Both or none.
	CertFile string
	KeyFile  string
	// ServerName overrides the name the server certificate is verified for.
	ServerName string
}

// TLSConfig reads the files of c and returns the client TLS configuration.
func (c ClientConfig) TLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: MinTLSVersion, ServerName: c.ServerName}
	if c.CAFile != "" {
		pool, err := loadPool(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("security: server CA: %w", err)
		}
		cfg.RootCAs = pool
	}
	switch {
	case c.CertFile != "" && c.KeyFile != "":
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("security: load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	case c.CertFile != "" || c.KeyFile != "":
		return nil, errors.New("security: client certificate and key files must be set together")
	}
	return cfg, nil
}

// Identity returns the identity of a consumer from its TLS connection state: the common name
// of a client certificate the server verified, or access.Anonymous.
func Identity(state *tls.ConnectionState) access.Identity {
	if state == nil || len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		return access.Anonymous
	}
	return access.Identity{Authenticated: true, Name: state.VerifiedChains[0][0].Subject.CommonName}
}

func loadPool(file string) (*x509.CertPool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("%s holds no PEM certificate", file)
	}
	return pool, nil
}
