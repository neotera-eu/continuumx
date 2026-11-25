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

package security_test

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/testing/testpki"
)

type pki struct {
	ca, other     *testpki.CA
	server        testpki.Pair
	client        testpki.Pair
	foreignClient testpki.Pair
}

func newPKI(t *testing.T) pki {
	ca, other := testpki.NewCA(t, "publisher-ca"), testpki.NewCA(t, "other-ca")
	return pki{
		ca: ca, other: other,
		server:        ca.Server("dtpublisher"),
		client:        ca.Client("consumer-a"),
		foreignClient: other.Client("consumer-a"),
	}
}

func TestServerConfigValidate(t *testing.T) {
	p := newPKI(t)
	ok := security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile}
	require.NoError(t, ok.Validate())

	for name, c := range map[string]security.ServerConfig{
		"no certificate":         {KeyFile: p.server.KeyFile},
		"no key":                 {CertFile: p.server.CertFile},
		"CA without client auth": {CertFile: p.server.CertFile, KeyFile: p.server.KeyFile, ClientCAFile: p.ca.CertFile},
		"require without CA":     {CertFile: p.server.CertFile, KeyFile: p.server.KeyFile, ClientAuth: security.ClientAuthRequire},
		"optional without CA":    {CertFile: p.server.CertFile, KeyFile: p.server.KeyFile, ClientAuth: security.ClientAuthOptional},
		"unknown mode":           {CertFile: p.server.CertFile, KeyFile: p.server.KeyFile, ClientCAFile: p.ca.CertFile, ClientAuth: "always"},
	} {
		assert.Error(t, c.Validate(), name)
		_, err := c.TLSConfig()
		assert.Error(t, err, name)
	}
}

func TestServerConfigFiles(t *testing.T) {
	p := newPKI(t)
	_, err := security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.client.KeyFile}.TLSConfig()
	assert.ErrorContains(t, err, "load server certificate", "key does not match")

	notPEM := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(notPEM, []byte("not a certificate"), 0o600))
	_, err = security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile, ClientCAFile: notPEM, ClientAuth: security.ClientAuthRequire}.TLSConfig()
	assert.ErrorContains(t, err, "no PEM certificate")

	cfg, err := security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile}.TLSConfig()
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	assert.Equal(t, tls.NoClientCert, cfg.ClientAuth)
}

func TestClientConfig(t *testing.T) {
	p := newPKI(t)
	_, err := security.ClientConfig{CertFile: p.client.CertFile}.TLSConfig()
	assert.ErrorContains(t, err, "together")
	_, err = security.ClientConfig{CAFile: filepath.Join(t.TempDir(), "missing.crt")}.TLSConfig()
	assert.Error(t, err)

	cfg, err := security.ClientConfig{CAFile: p.ca.CertFile, CertFile: p.client.CertFile, KeyFile: p.client.KeyFile, ServerName: "localhost"}.TLSConfig()
	require.NoError(t, err)
	assert.Len(t, cfg.Certificates, 1)
	assert.Equal(t, "localhost", cfg.ServerName)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
}

func TestIdentityWithoutTLS(t *testing.T) {
	assert.False(t, security.Identity(nil).Authenticated)
	assert.False(t, security.Identity(&tls.ConnectionState{}).Authenticated)
}

// serve starts an HTTPS server with cfg that answers with the consumer identity.
func serve(t *testing.T, cfg security.ServerConfig) string {
	t.Helper()
	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, security.Identity(r.TLS).String())
		}),
		TLSConfig: tlsCfg,
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return "https://" + ln.Addr().String()
}

// call makes a request with a client of cfg and returns the identity the server saw.
func call(t *testing.T, url string, cfg security.ClientConfig) (string, error) {
	t.Helper()
	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)
	return callTLS(t, url, tlsCfg)
}

// callForcing is call with a client that sends its certificate even when the server does not
// list its CA as acceptable. A Go client would otherwise send none.
func callForcing(t *testing.T, url string, cfg security.ClientConfig) (string, error) {
	t.Helper()
	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)
	cert := tlsCfg.Certificates[0]
	tlsCfg.Certificates = nil
	tlsCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &cert, nil }
	return callTLS(t, url, tlsCfg)
}

func callTLS(t *testing.T, url string, tlsCfg *tls.Config) (string, error) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer hc.CloseIdleConnections()
	resp, err := hc.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func TestHandshakes(t *testing.T) {
	p := newPKI(t)
	server := security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile, ClientCAFile: p.ca.CertFile}
	trusting := security.ClientConfig{CAFile: p.ca.CertFile}
	withCert := security.ClientConfig{CAFile: p.ca.CertFile, CertFile: p.client.CertFile, KeyFile: p.client.KeyFile}
	foreign := security.ClientConfig{CAFile: p.ca.CertFile, CertFile: p.foreignClient.CertFile, KeyFile: p.foreignClient.KeyFile}

	t.Run("require", func(t *testing.T) {
		cfg := server
		cfg.ClientAuth = security.ClientAuthRequire
		url := serve(t, cfg)

		id, err := call(t, url, withCert)
		require.NoError(t, err)
		assert.Equal(t, `"consumer-a"`, id)

		_, err = call(t, url, trusting)
		assert.Error(t, err, "no client certificate")
		_, err = callForcing(t, url, foreign)
		assert.ErrorContains(t, err, "unknown certificate authority", "client certificate of an unknown CA")
	})

	t.Run("optional", func(t *testing.T) {
		cfg := server
		cfg.ClientAuth = security.ClientAuthOptional
		url := serve(t, cfg)

		id, err := call(t, url, withCert)
		require.NoError(t, err)
		assert.Equal(t, `"consumer-a"`, id)

		id, err = call(t, url, trusting)
		require.NoError(t, err)
		assert.Equal(t, "anonymous", id)

		_, err = callForcing(t, url, foreign)
		assert.ErrorContains(t, err, "unknown certificate authority", "a presented certificate is still verified")
	})

	t.Run("none", func(t *testing.T) {
		url := serve(t, security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile})
		id, err := call(t, url, withCert)
		require.NoError(t, err)
		assert.Equal(t, "anonymous", id, "a client certificate is not asked for")
	})

	t.Run("server verification", func(t *testing.T) {
		url := serve(t, security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile})
		_, err := call(t, url, security.ClientConfig{CAFile: p.other.CertFile})
		assert.Error(t, err, "server certificate of an untrusted CA")
	})

	t.Run("old TLS version", func(t *testing.T) {
		url := serve(t, security.ServerConfig{CertFile: p.server.CertFile, KeyFile: p.server.KeyFile})
		tlsCfg, err := trusting.TLSConfig()
		require.NoError(t, err)
		tlsCfg.MinVersion, tlsCfg.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
		hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
		_, err = hc.Get(url)
		assert.Error(t, err, "TLS 1.1 is refused")
	})
}
