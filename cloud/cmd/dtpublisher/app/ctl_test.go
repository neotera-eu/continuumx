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

package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

// scripted serves one canned response per watch request, in order, and records each query.
type scripted struct {
	mu        sync.Mutex
	responses []func(w http.ResponseWriter)
	queries   []string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.queries)
	s.queries = append(s.queries, r.URL.RawQuery)
	s.mu.Unlock()
	if n >= len(s.responses) {
		http.Error(w, "no more responses", http.StatusGone)
		return
	}
	s.responses[n](w)
}

func (s *scripted) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func stream(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

func refuse(status int, reason string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"reason":"`+reason+`","error":"refused"}`)
	}
}

func fastReconnect(t *testing.T) {
	oldMin, oldMax := reconnectMin, reconnectMax
	reconnectMin, reconnectMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { reconnectMin, reconnectMax = oldMin, oldMax })
}

const (
	syncedAt3  = "event: SYNCED\nid: e1:3\ndata: {\"epoch\":\"e1\",\"revision\":3}\n\n"
	upsertAt4  = "event: UPSERT\nid: e1:4\ndata: {\"kind\":\"Node\",\"key\":\"nodes/edge-worker/n\",\"revision\":4,\"spec\":{\"name\":\"n\"}}\n\n"
	overflowAt = "event: ERROR\ndata: {\"reason\":\"overflow\",\"message\":\"too slow\",\"epoch\":\"e1\",\"revision\":4}\n\n"
)

func TestWatchReconnectsFromLastEvent(t *testing.T) {
	fastReconnect(t)
	srv := &scripted{responses: []func(http.ResponseWriter){
		stream(syncedAt3),              // ends without ERROR
		stream(upsertAt4 + overflowAt), // resumed from 3, then cut off
		stream(syncedAt3),              // resumed from 4
	}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	out, errOut, err := ctl(context.Background(), "--server", ts.URL, "watch", "nodes/", "-o", "text", "--max-events", "4")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"prefix=nodes%2F",
		"epoch=e1&prefix=nodes%2F&since=3",
		"epoch=e1&prefix=nodes%2F&since=4",
	}, srv.recorded())

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 4)
	assert.Contains(t, lines[0], "SYNCED")
	assert.Contains(t, lines[1], "UPSERT    nodes/edge-worker/n rev=4")
	assert.Contains(t, lines[2], "ERROR     overflow: too slow (resume e1:4)")
	assert.Contains(t, lines[3], "SYNCED")
	assert.Contains(t, errOut, "reconnecting in 1ms from e1:3")
	assert.Contains(t, errOut, "from e1:4")
}

func TestWatchFromGivenCursor(t *testing.T) {
	srv := &scripted{responses: []func(http.ResponseWriter){stream(upsertAt4)}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	out, _, err := ctl(context.Background(), "--server", ts.URL, "watch", "--since", "3", "--epoch", "e1", "--max-events", "1")
	require.NoError(t, err)
	assert.Equal(t, []string{"epoch=e1&since=3"}, srv.recorded())
	lines := parseWatchLines(t, out)
	require.Len(t, lines, 1)
	assert.Equal(t, "UPSERT", lines[0].Event)
	assert.Equal(t, "e1:4", lines[0].ID)
	assert.JSONEq(t, `{"kind":"Node","key":"nodes/edge-worker/n","revision":4,"spec":{"name":"n"}}`, string(lines[0].Data))
}

func TestWatchRefusalIsNotRetried(t *testing.T) {
	fastReconnect(t)
	srv := &scripted{responses: []func(http.ResponseWriter){refuse(http.StatusForbidden, "Forbidden")}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	_, _, err := ctl(context.Background(), "--server", ts.URL, "watch", "nodes/")
	assert.ErrorContains(t, err, "Forbidden")
	assert.Len(t, srv.recorded(), 1)
}

func TestWatchRetriesServerErrors(t *testing.T) {
	fastReconnect(t)
	srv := &scripted{responses: []func(http.ResponseWriter){
		refuse(http.StatusServiceUnavailable, ""),
		stream(syncedAt3),
	}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	_, errOut, err := ctl(context.Background(), "--server", ts.URL, "watch", "--max-events", "1")
	require.NoError(t, err)
	assert.Len(t, srv.recorded(), 2)
	assert.Contains(t, errOut, "503")
}

func TestWatchNoReconnect(t *testing.T) {
	srv := &scripted{responses: []func(http.ResponseWriter){stream(syncedAt3)}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	_, _, err := ctl(context.Background(), "--server", ts.URL, "watch", "--no-reconnect")
	assert.ErrorContains(t, err, "publisher ended the stream")
	assert.Len(t, srv.recorded(), 1)
}

func TestWatchStopsOnCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, syncedAt3)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := ctl(ctx, "--server", ts.URL, "watch")
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "an interrupted watch is a normal stop")
	case <-time.After(waitTimeout):
		t.Fatal("watch did not stop")
	}
}

func TestCtlArguments(t *testing.T) {
	bg := context.Background()
	server := []string{"--server", "http://127.0.0.1:1"}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "device", "only-a-name"}, "want NAMESPACE/NAME"},
		{[]string{"get", "device", "a/b/c"}, "want NAMESPACE/NAME"},
		{[]string{"get", "node"}, "accepts 1 arg"},
		{[]string{"watch", "--since", "3"}, "--since needs --epoch"},
		{[]string{"list", "nodes", "-o", "text"}, "not supported"},
		{[]string{"watch", "-o", "table"}, "not supported"},
		{[]string{"capabilities", "-o", "table"}, "not supported"},
	} {
		_, _, err := ctl(bg, append(server, tc.args...)...)
		assert.ErrorContains(t, err, tc.want, "%v", tc.args)
	}

	_, _, err := ctl(bg, "--server", "ftp://host", "capabilities")
	assert.ErrorContains(t, err, "want http(s)://host")
	_, _, err = ctl(bg, "--server", "https://host:1", "--cert-file", "/x.crt", "capabilities")
	assert.ErrorContains(t, err, "together")
}

func TestCtlServerFromEnvironment(t *testing.T) {
	srv := &scripted{responses: []func(http.ResponseWriter){refuse(http.StatusNotFound, "QueryDisabled")}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	t.Setenv(EnvServer, ts.URL)

	_, _, err := ctl(context.Background(), "list", "nodes")
	assert.ErrorContains(t, err, "start the publisher with --enable-query")
	assert.Len(t, srv.recorded(), 1)
}

func TestPrintTable(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, printTable(&out, nil))
	assert.Equal(t, "No resources.\n", out.String())

	out.Reset()
	require.NoError(t, printTable(&out, []model.Resource{
		{Kind: model.KindDevice, Key: "devices/default/d", Revision: 7, Spec: &model.DeviceSpec{Namespace: "default", Name: "d"}},
		{Kind: "EnergyMeter", Key: "meters/m", Revision: 8},
	}))
	assert.Equal(t, "NAMESPACE  NAME  NODE  STATE  PROPERTIES  REVISION\n"+
		"default    d     -     -      0           7\n"+
		"\n"+
		"KEY       KIND         REVISION\n"+
		"meters/m  EnergyMeter  8\n", out.String())
}
