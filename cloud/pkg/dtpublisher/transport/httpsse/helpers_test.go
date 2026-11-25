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
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

const (
	epoch       = "e2e5a1"
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

func tire(pressure string) model.Resource {
	return model.Resource{
		Kind: model.KindDevice,
		Key:  deviceKey,
		Spec: &model.DeviceSpec{
			Namespace: "default",
			Name:      "automotive-tire-pressure-ecu-fw-001",
			Node:      "u2deb-edge",
			Properties: []model.DeviceProperty{{
				Name: "frontLeftPressure", Type: "FLOAT", Unit: "kPa",
				Reported: &model.TwinValue{Value: pressure},
			}},
		},
	}
}

func mustUpsert(t *testing.T, st *store.Store, r model.Resource) {
	t.Helper()
	changed, err := st.Upsert(r)
	require.NoError(t, err)
	require.True(t, changed)
}

// newStore returns a store holding the two playground nodes (revisions 1, 2) and the tire
// device (revision 3).
func newStore(t *testing.T) *store.Store {
	st := store.New(store.WithEpoch(epoch))
	mustUpsert(t, st, node("u2deb-edge", model.NodeCategoryEdgeWorker, "True"))
	mustUpsert(t, st, node("u2deb-cloud", model.NodeCategoryCloudControlPlane, "True"))
	mustUpsert(t, st, tire("230.0"))
	return st
}

func newServer(t *testing.T, st *store.Store, opts httpsse.Options) *httpsse.Server {
	t.Helper()
	opts.Insecure = true
	srv, err := httpsse.New(opts, transport.Deps{Store: st, Info: transport.Info{
		Kinds: []transport.KindInfo{
			{Kind: model.KindNode, Prefix: model.NodesPrefix + "/", Categories: []string{"cloud-control-plane", "cloud-worker", "edge-worker"}},
			{Kind: model.KindDevice, Prefix: model.DevicesPrefix + "/"},
		},
		MinPublishInterval: time.Second,
	}})
	require.NoError(t, err)
	return srv
}

func startHTTP(t *testing.T, st *store.Store, opts httpsse.Options) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(newServer(t, st, opts).Handler())
	t.Cleanup(ts.Close)
	return ts
}

// sseEvent is one parsed Server-Sent Event.
type sseEvent struct {
	Event string
	ID    string
	Data  string
}

func (e sseEvent) resource(t *testing.T) model.Resource {
	t.Helper()
	var r struct {
		model.Resource
		Spec json.RawMessage `json:"spec"`
	}
	require.NoError(t, json.Unmarshal([]byte(e.Data), &r))
	r.Resource.Spec = r.Spec
	return r.Resource
}

func (e sseEvent) cursor(t *testing.T) httpsse.Cursor {
	t.Helper()
	var c httpsse.Cursor
	require.NoError(t, json.Unmarshal([]byte(e.Data), &c))
	return c
}

func (e sseEvent) deletion(t *testing.T) httpsse.Deletion {
	t.Helper()
	var d httpsse.Deletion
	require.NoError(t, json.Unmarshal([]byte(e.Data), &d))
	return d
}

// readEvents parses an SSE stream into events until it ends.
func readEvents(r io.Reader, out chan<- sseEvent) {
	defer close(out)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var ev sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if ev.Event != "" || ev.Data != "" {
				out <- ev
			}
			ev = sseEvent{}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.Event = value
		case "id":
			ev.ID = value
		case "data":
			ev.Data += value
		}
	}
}

type stream struct {
	resp   *http.Response
	events chan sseEvent
}

// watch opens a watch stream. header may be nil.
func watch(t *testing.T, baseURL, query string, header http.Header) *stream {
	t.Helper()
	url := baseURL + httpsse.WatchPath
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	s := &stream{resp: resp, events: make(chan sseEvent, 1024)}
	go readEvents(resp.Body, s.events)
	return s
}

func (s *stream) next(t *testing.T) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		require.True(t, ok, "stream ended")
		return ev
	case <-time.After(waitTimeout):
		t.Fatal("no event")
		return sseEvent{}
	}
}

// nextChange skips HEARTBEAT events.
func (s *stream) nextChange(t *testing.T) sseEvent {
	t.Helper()
	for {
		if ev := s.next(t); ev.Event != httpsse.EventHeartbeat {
			return ev
		}
	}
}

// ended waits until the stream ends.
func (s *stream) ended(t *testing.T) {
	t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case _, ok := <-s.events:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("stream did not end")
		}
	}
}

// summary is the event name and key or cursor revision, for compact assertions.
type summary struct {
	Event string
	Key   string
	ID    string
}

func (s *stream) summarize(t *testing.T, n int) []summary {
	t.Helper()
	out := make([]summary, 0, n)
	for i := 0; i < n; i++ {
		ev := s.nextChange(t)
		sm := summary{Event: ev.Event, ID: ev.ID}
		switch ev.Event {
		case httpsse.EventUpsert:
			sm.Key = ev.resource(t).Key
		case httpsse.EventDelete:
			sm.Key = ev.deletion(t).Key
		}
		out = append(out, sm)
	}
	return out
}
