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
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

func TestNewValidatesOptions(t *testing.T) {
	deps := transport.Deps{Store: store.New()}

	_, err := httpsse.New(httpsse.Options{}, deps)
	assert.ErrorContains(t, err, "TLS is required")

	_, err = httpsse.New(httpsse.Options{Insecure: true, TLSConfig: &tls.Config{}}, deps)
	assert.ErrorContains(t, err, "mutually exclusive")

	_, err = httpsse.New(httpsse.Options{Insecure: true}, transport.Deps{})
	assert.ErrorContains(t, err, "store")

	srv, err := httpsse.New(httpsse.Options{Insecure: true}, deps)
	require.NoError(t, err)
	assert.Equal(t, "httpsse", srv.Name())
	assert.ErrorContains(t, srv.Run(context.Background()), "listen address")
}

func TestCapabilities(t *testing.T) {
	ts := startHTTP(t, newStore(t), httpsse.Options{})

	resp, err := http.Get(ts.URL + httpsse.BasePath)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var c transport.Capabilities
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&c))
	assert.Equal(t, model.SchemaVersion, c.SchemaVersion)
	assert.Equal(t, "httpsse", c.Transport)
	assert.Equal(t, epoch, c.Epoch)
	assert.Equal(t, uint64(3), c.Revision)
	assert.Equal(t, transport.Operations{Watch: true}, c.Operations, "list and get are not served")
	assert.Equal(t, "1s", c.MinPublishInterval)
	require.Len(t, c.Kinds, 2)
	assert.Equal(t, model.KindNode, c.Kinds[0].Kind)
	assert.Equal(t, "nodes/", c.Kinds[0].Prefix)
	assert.Equal(t, []string{"cloud-control-plane", "cloud-worker", "edge-worker"}, c.Kinds[0].Categories)
}

func TestUnservedRequests(t *testing.T) {
	ts := startHTTP(t, newStore(t), httpsse.Options{})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, httpsse.BasePath, http.StatusMethodNotAllowed},
		{http.MethodDelete, httpsse.WatchPath, http.StatusMethodNotAllowed},
		{http.MethodGet, httpsse.BasePath + "/nodes", http.StatusNotFound},
		{http.MethodGet, httpsse.BasePath + "/devices/default/x", http.StatusNotFound},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodGet, httpsse.WatchPath + "?prefix=nodes/*", http.StatusBadRequest},
		{http.MethodGet, httpsse.WatchPath + "?since=abc", http.StatusBadRequest},
	} {
		req, err := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, tc.status, resp.StatusCode, "%s %s", tc.method, tc.path)
	}
}

func TestWatchInitialStateThenChanges(t *testing.T) {
	st := newStore(t)
	ts := startHTTP(t, st, httpsse.Options{})
	s := watch(t, ts.URL, "", nil)

	reset := s.next(t)
	assert.Equal(t, httpsse.EventReset, reset.Event)
	assert.Empty(t, reset.ID)
	assert.Equal(t, httpsse.Cursor{Epoch: epoch, Revision: 3}, reset.cursor(t))

	// Current state in key order, without IDs, then SYNCED with the cursor.
	assert.Equal(t, []summary{
		{Event: httpsse.EventUpsert, Key: deviceKey},
		{Event: httpsse.EventUpsert, Key: cloudKey},
		{Event: httpsse.EventUpsert, Key: edgeKey},
	}, s.summarize(t, 3))
	synced := s.next(t)
	assert.Equal(t, httpsse.EventSynced, synced.Event)
	assert.Equal(t, epoch+":3", synced.ID)

	// Live changes carry IDs.
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "Unknown"))
	ev := s.nextChange(t)
	assert.Equal(t, httpsse.EventUpsert, ev.Event)
	assert.Equal(t, epoch+":4", ev.ID)
	r := ev.resource(t)
	assert.Equal(t, model.SchemaVersion, r.SchemaVersion)
	assert.Equal(t, model.KindNode, r.Kind)
	assert.Equal(t, uint64(4), r.Revision)
	assert.JSONEq(t, `{"name":"u2deb-edge","category":"edge-worker","ready":"Unknown","nodeInfo":{}}`, string(r.Spec.(json.RawMessage)))

	require.True(t, st.Delete(edgeKey))
	ev = s.nextChange(t)
	assert.Equal(t, httpsse.EventDelete, ev.Event)
	assert.Equal(t, epoch+":5", ev.ID)
	assert.Equal(t, httpsse.Deletion{Key: edgeKey, Revision: 5}, ev.deletion(t))
}

func TestWatchPrefix(t *testing.T) {
	st := newStore(t)
	ts := startHTTP(t, st, httpsse.Options{})
	s := watch(t, ts.URL, "prefix=devices/", nil)

	assert.Equal(t, []summary{
		{Event: httpsse.EventReset},
		{Event: httpsse.EventUpsert, Key: deviceKey},
		{Event: httpsse.EventSynced, ID: epoch + ":3"},
	}, s.summarize(t, 3))

	// A node change is not delivered; the next device change is.
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "Unknown")) // 4
	mustUpsert(t, st, tire("225.0"))                                               // 5
	ev := s.nextChange(t)
	assert.Equal(t, epoch+":5", ev.ID)
	assert.Equal(t, deviceKey, ev.resource(t).Key)
}

func TestWatchResume(t *testing.T) {
	st := newStore(t)
	ts := startHTTP(t, st, httpsse.Options{})
	mustUpsert(t, st, tire("225.0"))    // 4
	require.True(t, st.Delete(edgeKey)) // 5

	// Resumed from revision 3 of this epoch: only what changed since.
	s := watch(t, ts.URL, "since=3&epoch="+epoch, nil)
	assert.Equal(t, []summary{
		{Event: httpsse.EventUpsert, Key: deviceKey},
		{Event: httpsse.EventDelete, Key: edgeKey},
		{Event: httpsse.EventSynced, ID: epoch + ":5"},
	}, s.summarize(t, 3))

	// The same through the standard Last-Event-ID header, with a prefix.
	s = watch(t, ts.URL, "prefix=nodes/", http.Header{httpsse.HeaderLastEventID: {epoch + ":4"}})
	assert.Equal(t, []summary{
		{Event: httpsse.EventDelete, Key: edgeKey},
		{Event: httpsse.EventSynced, ID: epoch + ":5"},
	}, s.summarize(t, 2))

	// Another epoch, a missing epoch, a revision ahead of the store or an unreadable header:
	// a fresh start.
	for _, tc := range []struct {
		query  string
		header http.Header
	}{
		{query: "since=3&epoch=other"},
		{query: "since=3"},
		{query: "since=99&epoch=" + epoch},
		{header: http.Header{httpsse.HeaderLastEventID: {"garbage"}}},
	} {
		s := watch(t, ts.URL, tc.query, tc.header)
		assert.Equal(t, []summary{
			{Event: httpsse.EventReset},
			{Event: httpsse.EventUpsert, Key: deviceKey},
			{Event: httpsse.EventUpsert, Key: cloudKey},
			{Event: httpsse.EventSynced, ID: epoch + ":5"},
		}, s.summarize(t, 4), "query %q header %v", tc.query, tc.header)
	}
}

func TestWatchHeartbeat(t *testing.T) {
	st := newStore(t)
	ts := startHTTP(t, st, httpsse.Options{HeartbeatInterval: 20 * time.Millisecond})
	s := watch(t, ts.URL, "prefix=devices/", nil)
	s.summarize(t, 3)

	// A node change outside the prefix does not move the subscriber's cursor.
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "Unknown"))
	ev := s.next(t)
	assert.Equal(t, httpsse.EventHeartbeat, ev.Event)
	assert.Equal(t, epoch+":3", ev.ID)
	assert.Equal(t, httpsse.Cursor{Epoch: epoch, Revision: 3}, ev.cursor(t))

	mustUpsert(t, st, tire("225.0"))
	assert.Equal(t, epoch+":5", s.nextChange(t).ID)
	for {
		if ev := s.next(t); ev.Event == httpsse.EventHeartbeat {
			assert.Equal(t, epoch+":5", ev.ID)
			break
		}
	}
}

func TestServeStopsWithOpenStreams(t *testing.T) {
	st := newStore(t)
	srv := newServer(t, st, httpsse.Options{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	s := watch(t, "http://"+ln.Addr().String(), "", nil)
	s.summarize(t, 5)
	mustUpsert(t, st, tire("225.0"))
	assert.Equal(t, epoch+":4", s.nextChange(t).ID)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(waitTimeout):
		t.Fatal("Serve did not stop")
	}
	s.ended(t)
}
