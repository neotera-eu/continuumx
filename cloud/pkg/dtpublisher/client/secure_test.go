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

package client_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/client"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/internal/testpki"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

func TestMutualTLSAndAccess(t *testing.T) {
	ca := testpki.NewCA(t, "publisher-ca")
	server := ca.Server("dtpublisher")
	tlsCfg, err := security.ServerConfig{
		CertFile: server.CertFile, KeyFile: server.KeyFile,
		ClientCAFile: ca.CertFile, ClientAuth: security.ClientAuthOptional,
	}.TLSConfig()
	require.NoError(t, err)
	authz, err := access.NewStatic([]access.Consumer{{Name: "consumer-a", Prefixes: []string{"devices/"}}})
	require.NoError(t, err)

	st := store.New(store.WithEpoch(epoch))
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "True"))
	mustUpsert(t, st, tire("230.0", "false"))
	srv, err := httpsse.New(httpsse.Options{TLSConfig: tlsCfg, QueryEnabled: true, Authorizer: authz}, transport.Deps{Store: st})
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() {
		cancel()
		assert.NoError(t, <-done)
	}()

	newClient := func(name string) *client.Client {
		cfg := security.ClientConfig{CAFile: ca.CertFile}
		if name != "" {
			pair := ca.Client(name)
			cfg.CertFile, cfg.KeyFile = pair.CertFile, pair.KeyFile
		}
		clientTLS, err := cfg.TLSConfig()
		require.NoError(t, err)
		c, err := client.New("https://"+ln.Addr().String(), client.WithTLS(clientTLS))
		require.NoError(t, err)
		return c
	}
	bg := context.Background()

	_, err = newClient("").Capabilities(bg)
	assert.ErrorIs(t, err, client.ErrUnauthenticated)
	_, err = newClient("consumer-x").Capabilities(bg)
	assert.ErrorIs(t, err, client.ErrForbidden)

	a := newClient("consumer-a")
	caps, err := a.Capabilities(bg)
	require.NoError(t, err)
	assert.Equal(t, epoch, caps.Epoch)

	_, err = a.GetNode(bg, "u2deb-edge")
	assert.ErrorIs(t, err, client.ErrForbidden)
	_, err = a.Watch(bg, client.WatchOptions{})
	assert.ErrorIs(t, err, client.ErrForbidden, "watching everything is broader than devices/")

	d, err := a.GetDevice(bg, "default", "automotive-tire-pressure-ecu-fw-001")
	require.NoError(t, err)
	assert.Equal(t, "u2deb-edge", d.Spec.(*model.DeviceSpec).Node)

	w, err := a.Watch(bg, client.WatchOptions{Prefix: "devices/"})
	require.NoError(t, err)
	defer w.Close()
	assert.Equal(t, httpsse.EventReset, next(t, w).Type)
	assert.Equal(t, deviceKey, next(t, w).Resource.Key)
	assert.Equal(t, httpsse.EventSynced, next(t, w).Type)
}
