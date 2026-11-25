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
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
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

// canned serves body as an event stream with the given content type.
func canned(t *testing.T, contentType, body string) *client.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	c, err := client.New(ts.URL)
	require.NoError(t, err)
	return c
}

func collect(t *testing.T, w *client.Watcher) []client.Event {
	t.Helper()
	var out []client.Event
	timeout := time.After(waitTimeout)
	for {
		select {
		case ev, ok := <-w.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("stream did not end")
		}
	}
}

func TestParseStreamFormat(t *testing.T) {
	const body = ": a comment\r\n" +
		"retry: 1000\r\n" +
		"event: SYNCED\r\n" +
		"id: e1:7\r\n" +
		"data: {\"epoch\":\"e1\",\"revision\":7}\r\n" +
		"\r\n" +
		"event: FUTURE\n" + // an event type this client does not know: kept raw
		"data: line one\n" +
		"data:line two\n" +
		"\n" +
		"event: NODATA\n" + // no data: not dispatched
		"\n" +
		"data: {\"x\":1}\n" + // no event type: "message"
		"\n" +
		"event: UPSERT\n" + // an unknown kind: spec kept raw
		"id: e1:8\n" +
		"data: {\"kind\":\"EnergyMeter\",\"key\":\"meters/a\",\"revision\":8,\"spec\":{\"kWh\":3}}\n" +
		"\n" +
		"event: ERROR\n" +
		"data: {\"reason\":\"overflow\",\"message\":\"too slow\",\"epoch\":\"e1\",\"revision\":8}\n" +
		"\n" +
		"event: UPSERT\n" + // after ERROR: not delivered
		"data: {}\n" +
		"\n"
	w, err := canned(t, "text/event-stream; charset=utf-8", body).Watch(context.Background(), client.WatchOptions{})
	require.NoError(t, err)
	events := collect(t, w)

	require.Len(t, events, 5)
	assert.Equal(t, httpsse.EventSynced, events[0].Type)
	assert.Equal(t, &httpsse.Cursor{Epoch: "e1", Revision: 7}, events[0].Cursor)
	assert.Equal(t, "FUTURE", events[1].Type)
	assert.Equal(t, "line one\nline two", string(events[1].Data))
	assert.Equal(t, "message", events[2].Type)
	assert.Equal(t, model.Kind("EnergyMeter"), events[3].Resource.Kind)
	assert.JSONEq(t, `{"kWh":3}`, string(events[3].Resource.Spec.(json.RawMessage)))
	assert.Equal(t, httpsse.ReasonOverflow, events[4].Error.Reason)

	var closed *client.StreamClosedError
	require.True(t, errors.As(w.Err(), &closed))
	assert.Equal(t, uint64(8), closed.Revision, "the cursor to resume from")
	cursor, ok := w.Cursor()
	require.True(t, ok)
	assert.Equal(t, httpsse.Cursor{Epoch: "e1", Revision: 8}, cursor)
}

func TestParseStreamDecodeError(t *testing.T) {
	w, err := canned(t, "text/event-stream", "event: DELETE\ndata: not json\n\n").Watch(context.Background(), client.WatchOptions{})
	require.NoError(t, err)
	assert.Empty(t, collect(t, w))
	assert.ErrorContains(t, w.Err(), "decode DELETE event")
}

func TestWatchEndedByPublisher(t *testing.T) {
	w, err := canned(t, "text/event-stream", "event: RESET\ndata: {\"epoch\":\"e1\",\"revision\":0}\n\n").Watch(context.Background(), client.WatchOptions{})
	require.NoError(t, err)
	assert.Len(t, collect(t, w), 1)
	assert.ErrorIs(t, w.Err(), client.ErrStreamEnded)
}

func TestWatchNotAnEventStream(t *testing.T) {
	_, err := canned(t, "application/json", "{}").Watch(context.Background(), client.WatchOptions{})
	assert.ErrorContains(t, err, "not an event stream")
}

func TestWatchPublisherShutdown(t *testing.T) {
	st := store.New()
	srv, err := httpsse.New(httpsse.Options{Insecure: true}, transport.Deps{Store: st})
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	c, err := client.New("http://" + ln.Addr().String())
	require.NoError(t, err)
	w, err := c.Watch(context.Background(), client.WatchOptions{})
	require.NoError(t, err)
	defer w.Close()
	assert.Equal(t, httpsse.EventReset, next(t, w).Type)
	assert.Equal(t, httpsse.EventSynced, next(t, w).Type)

	cancel()
	ended(t, w)
	assert.ErrorIs(t, w.Err(), client.ErrStreamEnded)
	require.NoError(t, <-done)
}
