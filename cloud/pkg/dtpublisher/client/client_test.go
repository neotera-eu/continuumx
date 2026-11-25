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
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/client"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

const (
	epoch       = "c11e47"
	waitTimeout = 5 * time.Second

	edgeKey   = "nodes/edge-worker/u2deb-edge"
	cloudKey  = "nodes/cloud-control-plane/u2deb-cloud"
	deviceKey = "devices/default/automotive-tire-pressure-ecu-fw-001"
)

func node(name string, category model.NodeCategory, ready string) model.Resource {
	return model.Resource{
		Kind: model.KindNode,
		Key:  model.NodeKey(category, name),
		Spec: &model.NodeSpec{Name: name, Category: category, Ready: ready},
	}
}

func tire(pressure, warning string) model.Resource {
	return model.Resource{
		Kind: model.KindDevice,
		Key:  deviceKey,
		Spec: &model.DeviceSpec{
			Namespace: "default",
			Name:      "automotive-tire-pressure-ecu-fw-001",
			Node:      "u2deb-edge",
			Properties: []model.DeviceProperty{
				{Name: "frontLeftPressure", Type: "FLOAT", Unit: "kPa", Reported: &model.TwinValue{Value: pressure}},
				{Name: "lowPressureWarning", Type: "BOOLEAN", Reported: &model.TwinValue{Value: warning}},
			},
		},
	}
}

func mustUpsert(t *testing.T, st *store.Store, r model.Resource) {
	t.Helper()
	changed, err := st.Upsert(r)
	require.NoError(t, err)
	require.True(t, changed)
}

// publisher starts an httpsse publisher holding the playground nodes (revisions 1, 2) and the
// tire device (revision 3), and returns its store and a client of it.
func publisher(t *testing.T, opts httpsse.Options) (*store.Store, *client.Client) {
	t.Helper()
	st := store.New(store.WithEpoch(epoch))
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "True"))
	mustUpsert(t, st, node("u2deb-cloud", model.NodeCategoryCloudControlPlane, "True"))
	mustUpsert(t, st, tire("230.0", "false"))

	opts.Insecure = true
	srv, err := httpsse.New(opts, transport.Deps{Store: st})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	c, err := client.New(ts.URL + "/")
	require.NoError(t, err)
	return st, c
}

func keys(resources []model.Resource) []string {
	out := []string{}
	for _, r := range resources {
		out = append(out, r.Key)
	}
	return out
}

func TestNewValidatesURL(t *testing.T) {
	for _, u := range []string{"", "dtpublisher:8443", "ftp://host", "http://", "http://host?x=1", "http://host#f", "://"} {
		_, err := client.New(u)
		assert.Error(t, err, u)
	}
	_, err := client.New("https://dtpublisher.continuumx:8443")
	assert.NoError(t, err)
}

func TestCapabilities(t *testing.T) {
	_, c := publisher(t, httpsse.Options{})
	caps, err := c.Capabilities(context.Background())
	require.NoError(t, err)
	assert.Equal(t, epoch, caps.Epoch)
	assert.Equal(t, uint64(3), caps.Revision)
	assert.Equal(t, "httpsse", caps.Transport)
	assert.Equal(t, transport.Operations{Watch: true}, caps.Operations)
}

func TestQueryDisabled(t *testing.T) {
	_, c := publisher(t, httpsse.Options{})
	_, err := c.ListNodes(context.Background(), client.ListNodesOptions{})
	assert.ErrorIs(t, err, client.ErrQueryDisabled)
	assert.NotErrorIs(t, err, client.ErrNotFound)

	var apiErr *client.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 404, apiErr.StatusCode)
	assert.Contains(t, apiErr.Error(), "QueryDisabled")
}

func TestListAndGet(t *testing.T) {
	ctx := context.Background()
	_, c := publisher(t, httpsse.Options{QueryEnabled: true})

	nodes, err := c.ListNodes(ctx, client.ListNodesOptions{})
	require.NoError(t, err)
	assert.Equal(t, httpsse.Cursor{Epoch: epoch, Revision: 3}, nodes.Cursor)
	assert.Equal(t, []string{cloudKey, edgeKey}, keys(nodes.Items))
	spec, ok := nodes.Items[1].Spec.(*model.NodeSpec)
	require.True(t, ok, "typed node spec, got %T", nodes.Items[1].Spec)
	assert.Equal(t, "True", spec.Ready)

	edge, err := c.ListNodes(ctx, client.ListNodesOptions{Category: model.NodeCategoryEdgeWorker})
	require.NoError(t, err)
	assert.Equal(t, []string{edgeKey}, keys(edge.Items))

	_, err = c.ListNodes(ctx, client.ListNodesOptions{Category: "edge"})
	var apiErr *client.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, httpsse.ReasonBadRequest, apiErr.Reason)

	devices, err := c.ListDevices(ctx, client.ListDevicesOptions{Node: "u2deb-edge"})
	require.NoError(t, err)
	assert.Equal(t, []string{deviceKey}, keys(devices.Items))
	devices, err = c.ListDevices(ctx, client.ListDevicesOptions{Namespace: "other"})
	require.NoError(t, err)
	assert.Empty(t, devices.Items)

	n, err := c.GetNode(ctx, "u2deb-edge")
	require.NoError(t, err)
	assert.Equal(t, model.SchemaVersion, n.SchemaVersion)
	assert.Equal(t, uint64(1), n.Revision)
	assert.Equal(t, model.NodeCategoryEdgeWorker, n.Spec.(*model.NodeSpec).Category)

	d, err := c.GetDevice(ctx, "default", "automotive-tire-pressure-ecu-fw-001", "lowPressureWarning")
	require.NoError(t, err)
	props := d.Spec.(*model.DeviceSpec).Properties
	require.Len(t, props, 1)
	assert.Equal(t, "false", props[0].Reported.Value)

	_, err = c.GetDevice(ctx, "default", "automotive-tire-pressure-ecu-fw-001", "rearLeftPressure")
	assert.ErrorIs(t, err, client.ErrNotFound)
	_, err = c.GetNode(ctx, "missing")
	assert.ErrorIs(t, err, client.ErrNotFound)

	_, err = c.GetNode(ctx, "")
	assert.Error(t, err)
	_, err = c.GetDevice(ctx, "default", "")
	assert.Error(t, err)
}

func next(t *testing.T, w *client.Watcher) client.Event {
	t.Helper()
	select {
	case ev, ok := <-w.Events():
		require.True(t, ok, "stream ended: %v", w.Err())
		return ev
	case <-time.After(waitTimeout):
		t.Fatal("no event")
		return client.Event{}
	}
}

func ended(t *testing.T, w *client.Watcher) {
	t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("stream did not end")
		}
	}
}

func TestWatchFreshThenLive(t *testing.T) {
	st, c := publisher(t, httpsse.Options{})
	w, err := c.Watch(context.Background(), client.WatchOptions{Prefix: "devices/"})
	require.NoError(t, err)
	defer w.Close()

	_, ok := w.Cursor()
	assert.False(t, ok, "no cursor before SYNCED")

	ev := next(t, w)
	assert.Equal(t, httpsse.EventReset, ev.Type)
	assert.Equal(t, &httpsse.Cursor{Epoch: epoch, Revision: 3}, ev.Cursor)

	ev = next(t, w)
	assert.Equal(t, httpsse.EventUpsert, ev.Type)
	assert.Empty(t, ev.ID)
	assert.Equal(t, "230.0", ev.Resource.Spec.(*model.DeviceSpec).Properties[0].Reported.Value)

	ev = next(t, w)
	assert.Equal(t, httpsse.EventSynced, ev.Type)
	assert.Equal(t, epoch+":3", ev.ID)

	mustUpsert(t, st, tire("232.5", "true"))
	ev = next(t, w)
	assert.Equal(t, httpsse.EventUpsert, ev.Type)
	assert.Equal(t, uint64(4), ev.Resource.Revision)
	assert.Equal(t, "true", ev.Resource.Spec.(*model.DeviceSpec).Properties[1].Reported.Value)

	require.True(t, st.Delete(deviceKey))
	ev = next(t, w)
	assert.Equal(t, &httpsse.Deletion{Key: deviceKey, Revision: 5}, ev.Deletion)
	assert.Equal(t, epoch+":5", ev.ID)

	w.Close()
	ended(t, w)
	assert.NoError(t, w.Err(), "closed by the client")
	cursor, ok := w.Cursor()
	require.True(t, ok)
	assert.Equal(t, httpsse.Cursor{Epoch: epoch, Revision: 5}, cursor)
}

func TestWatchResume(t *testing.T) {
	ctx := context.Background()
	st, c := publisher(t, httpsse.Options{QueryEnabled: true})

	list, err := c.ListDevices(ctx, client.ListDevicesOptions{})
	require.NoError(t, err)
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "Unknown")) // 4
	mustUpsert(t, st, tire("232.5", "true"))                                       // 5

	w, err := c.Watch(ctx, client.WatchOptions{Prefix: "devices/", Since: &list.Cursor})
	require.NoError(t, err)
	defer w.Close()
	cursor, ok := w.Cursor()
	require.True(t, ok, "the given cursor until a newer one arrives")
	assert.Equal(t, list.Cursor, cursor)

	ev := next(t, w)
	require.Equal(t, httpsse.EventUpsert, ev.Type)
	assert.Equal(t, uint64(5), ev.Resource.Revision)
	assert.Equal(t, httpsse.EventSynced, next(t, w).Type)
	w.Close()
	cursor, _ = w.Cursor()
	assert.Equal(t, uint64(5), cursor.Revision)
}

func TestWatchRejected(t *testing.T) {
	_, c := publisher(t, httpsse.Options{})
	_, err := c.Watch(context.Background(), client.WatchOptions{Prefix: "nodes/*"})
	var apiErr *client.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 400, apiErr.StatusCode)
	assert.Equal(t, httpsse.ReasonBadRequest, apiErr.Reason)
}

func TestWatchContextCancel(t *testing.T) {
	_, c := publisher(t, httpsse.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	w, err := c.Watch(ctx, client.WatchOptions{})
	require.NoError(t, err)
	next(t, w)

	cancel()
	ended(t, w)
	assert.NoError(t, w.Err())
}
