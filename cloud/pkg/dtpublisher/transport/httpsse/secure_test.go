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

package httpsse_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/testing/testpki"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

// secureServer serves st over TLS with client auth mode clientAuth and the given authorizer,
// and returns its URL and the CA that signs its certificates.
func secureServer(t *testing.T, st *store.Store, clientAuth security.ClientAuth, authz access.Authorizer) (string, *testpki.CA) {
	t.Helper()
	ca := testpki.NewCA(t, "publisher-ca")
	pair := ca.Server("dtpublisher")
	cfg := security.ServerConfig{CertFile: pair.CertFile, KeyFile: pair.KeyFile, ClientAuth: clientAuth}
	if clientAuth != security.ClientAuthNone {
		cfg.ClientCAFile = ca.CertFile
	}
	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)

	srv, err := httpsse.New(httpsse.Options{TLSConfig: tlsCfg, QueryEnabled: true, Authorizer: authz}, transport.Deps{Store: st})
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Error("Serve did not stop")
		}
	})
	return "https://" + ln.Addr().String(), ca
}

// consumer returns an HTTP client that trusts ca and, unless name is empty, presents a client
// certificate with common name name.
func consumer(t *testing.T, ca *testpki.CA, name string) *http.Client {
	t.Helper()
	cfg := security.ClientConfig{CAFile: ca.CertFile}
	if name != "" {
		pair := ca.Client(name)
		cfg.CertFile, cfg.KeyFile = pair.CertFile, pair.KeyFile
	}
	tlsCfg, err := cfg.TLSConfig()
	require.NoError(t, err)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	t.Cleanup(hc.CloseIdleConnections)
	return hc
}

func status(t *testing.T, hc *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := hc.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return resp.StatusCode, ""
	}
	var body httpsse.ErrorBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body.Reason
}

func TestMutualTLSRequired(t *testing.T) {
	url, ca := secureServer(t, newStore(t), security.ClientAuthRequire, nil)

	_, err := consumer(t, ca, "").Get(url + httpsse.BasePath)
	assert.Error(t, err, "no client certificate: the handshake fails")

	// Plain HTTP on the TLS port: the Go TLS server answers 400 and serves nothing.
	resp, err := http.Get("http://" + url[len("https://"):] + httpsse.BasePath)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	s := watchWith(t, consumer(t, ca, "consumer-a"), url, "prefix=devices/", nil)
	assert.Equal(t, []summary{
		{Event: httpsse.EventReset},
		{Event: httpsse.EventUpsert, Key: deviceKey},
		{Event: httpsse.EventSynced, ID: epoch + ":3"},
	}, s.summarize(t, 3))
}

func TestAccessControl(t *testing.T) {
	authz, err := access.NewStatic([]access.Consumer{
		{Name: "consumer-a", Prefixes: []string{"nodes/", "devices/"}},
		{Name: "consumer-b", Prefixes: []string{"devices/default/", "nodes/edge-worker/"}},
		{Name: "consumer-c", Prefixes: []string{"devices/"}},
	})
	require.NoError(t, err)
	st := newQueryStore(t)
	url, ca := secureServer(t, st, security.ClientAuthOptional, authz)
	anonymous, stranger := consumer(t, ca, ""), consumer(t, ca, "consumer-x")
	a, b, c := consumer(t, ca, "consumer-a"), consumer(t, ca, "consumer-b"), consumer(t, ca, "consumer-c")

	const (
		ok           = ""
		unauth       = httpsse.ReasonUnauthenticated
		forbidden    = httpsse.ReasonForbidden
		notFound     = httpsse.ReasonNotFound
		tirePath     = httpsse.DevicesPath + "/default/automotive-tire-pressure-ecu-fw-001"
		thermalPath  = httpsse.DevicesPath + "/vehicle-a/automotive-thermal-ecu-fw-001"
		edgeNodePath = httpsse.NodesPath + "/u2deb-edge"
	)
	codes := map[string]int{ok: 200, unauth: 401, forbidden: 403, notFound: 404}
	for _, tc := range []struct {
		name   string
		client *http.Client
		path   string
		reason string
	}{
		{"anonymous capabilities", anonymous, httpsse.BasePath, unauth},
		{"anonymous watch", anonymous, httpsse.WatchPath + "?prefix=devices/", unauth},
		{"anonymous get", anonymous, tirePath, unauth},
		{"stranger capabilities", stranger, httpsse.BasePath, forbidden},
		{"stranger watch", stranger, httpsse.WatchPath + "?prefix=devices/", forbidden},

		{"a capabilities", a, httpsse.BasePath, ok},
		{"a watch everything", a, httpsse.WatchPath, forbidden},
		{"a list nodes", a, httpsse.NodesPath, ok},
		{"a get thermal", a, thermalPath, ok},

		{"b watch devices", b, httpsse.WatchPath + "?prefix=devices/", forbidden},
		{"b list devices", b, httpsse.DevicesPath, forbidden},
		{"b list default devices", b, httpsse.DevicesPath + "?namespace=default", ok},
		{"b list devices of a node", b, httpsse.DevicesPath + "?node=u2deb-edge", forbidden},
		{"b get tire", b, tirePath, ok},
		{"b get thermal", b, thermalPath, forbidden},
		{"b list nodes", b, httpsse.NodesPath, forbidden},
		{"b list edge nodes", b, httpsse.NodesPath + "?category=edge-worker", ok},
		{"b get edge node", b, edgeNodePath, ok},
		// Only the edge-worker category is searched: the control-plane node is not revealed.
		{"b get cloud node", b, httpsse.NodesPath + "/u2deb-cloud", notFound},

		{"c get node", c, edgeNodePath, forbidden},
	} {
		code, reason := status(t, tc.client, url+tc.path)
		assert.Equal(t, codes[tc.reason], code, tc.name)
		assert.Equal(t, tc.reason, reason, tc.name)
	}

	// An allowed watch delivers only its prefix.
	s := watchWith(t, b, url, "prefix=devices/default/", nil)
	assert.Equal(t, []summary{
		{Event: httpsse.EventReset},
		{Event: httpsse.EventUpsert, Key: deviceKey},
		{Event: httpsse.EventSynced, ID: epoch + ":4"},
	}, s.summarize(t, 3))
}
