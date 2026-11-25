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

package transport_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/zenoh"
)

func newRegistry(t *testing.T) *transport.Registry {
	t.Helper()
	r := transport.NewRegistry()
	require.NoError(t, r.Register(zenoh.Name, zenoh.Factory(zenoh.Options{})))
	require.NoError(t, r.Register(httpsse.Name, httpsse.Factory(httpsse.Options{Insecure: true})))
	return r
}

func TestRegistry(t *testing.T) {
	r := newRegistry(t)
	deps := transport.Deps{Store: store.New()}
	assert.Equal(t, []string{"httpsse", "zenoh"}, r.Names())

	p, err := r.New("httpsse", deps)
	require.NoError(t, err)
	assert.Equal(t, "httpsse", p.Name())

	_, err = r.New("mqtt", deps)
	assert.ErrorIs(t, err, transport.ErrUnknownTransport)
	assert.ErrorContains(t, err, "[httpsse zenoh]")

	_, err = r.New("httpsse", transport.Deps{})
	assert.ErrorContains(t, err, "store")

	assert.Error(t, r.Register("httpsse", httpsse.Factory(httpsse.Options{})), "a name is registered once")
	assert.Error(t, r.Register("", httpsse.Factory(httpsse.Options{})))
	assert.Error(t, r.Register("x", nil))
}

func TestZenohIsNotImplemented(t *testing.T) {
	_, err := newRegistry(t).New("zenoh", transport.Deps{Store: store.New()})
	assert.ErrorIs(t, err, transport.ErrNotImplemented)
	assert.ErrorContains(t, err, "to be discussed with ZSC and DLR")
}

func TestNewCapabilities(t *testing.T) {
	st := store.New(store.WithEpoch("e1"))
	c := transport.NewCapabilities("httpsse", st, transport.Info{}, transport.Operations{Watch: true})
	assert.Equal(t, transport.Capabilities{
		SchemaVersion: model.SchemaVersion,
		Transport:     "httpsse",
		Epoch:         "e1",
		Operations:    transport.Operations{Watch: true},
		Kinds:         []transport.KindInfo{},
	}, c, "no kinds and no throttle")

	c = transport.NewCapabilities("httpsse", st, transport.Info{MinPublishInterval: 1500 * time.Millisecond}, transport.Operations{})
	assert.Equal(t, "1.5s", c.MinPublishInterval)
}
