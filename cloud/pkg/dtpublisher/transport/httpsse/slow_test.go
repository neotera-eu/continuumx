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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

// stalledWriter is a ResponseWriter whose writes block while it is stalled, like a subscriber
// that stopped reading.
type stalledWriter struct {
	header http.Header

	mu      sync.Mutex
	buf     bytes.Buffer
	stalled bool
	resume  chan struct{}
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{header: http.Header{}, resume: make(chan struct{})}
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Flush()              {}

func (w *stalledWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	stalled := w.stalled
	w.mu.Unlock()
	if stalled {
		<-w.resume
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *stalledWriter) stall() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stalled = true
}

func (w *stalledWriter) unstall() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stalled = false
	close(w.resume)
}

func (w *stalledWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestSlowSubscriberDoesNotBlockStoreOrOthers(t *testing.T) {
	const bufferSize, changes = 4, 50
	st := newStore(t)
	srv := newServer(t, st, httpsse.Options{BufferSize: bufferSize})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// The slow subscriber receives its initial state, then stops reading.
	slow := newStalledWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, httpsse.WatchPath+"?prefix=devices/", nil).WithContext(ctx)
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		srv.Handler().ServeHTTP(slow, req)
	}()
	require.Eventually(t, func() bool { return strings.Contains(slow.String(), "event: SYNCED") }, waitTimeout, 10*time.Millisecond)
	slow.stall()

	fast := watch(t, ts.URL, "prefix=devices/", nil)
	fast.summarize(t, 3)

	// The store accepts every change without waiting for the slow subscriber, and the fast
	// subscriber gets each one. Changes are published one at a time so the fast subscriber's
	// own buffer is not what is tested.
	for i := 0; i < changes; i++ {
		published := make(chan struct{})
		go func() {
			defer close(published)
			if _, err := st.Upsert(tire(fmt.Sprintf("%d.0", 229-i))); err != nil {
				t.Error(err)
			}
		}()
		select {
		case <-published:
		case <-time.After(waitTimeout):
			t.Fatal("the store blocked on a slow subscriber")
		}
		ev := fast.nextChange(t)
		require.Equal(t, fmt.Sprintf("%s:%d", epoch, 4+i), ev.ID)
	}

	// The slow subscriber, once it reads again, gets what fitted in its buffer, then ERROR
	// with the cursor to resume from, and its stream ends.
	slow.unstall()
	select {
	case <-handlerDone:
	case <-time.After(waitTimeout):
		t.Fatal("the slow subscriber's stream did not end")
	}
	out := slow.String()
	require.Contains(t, out, "event: ERROR")
	errData := out[strings.LastIndex(out, "data: ")+len("data: "):]
	var streamErr httpsse.StreamError
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(errData)), &streamErr))
	assert.Equal(t, httpsse.ReasonOverflow, streamErr.Reason)
	assert.Equal(t, epoch, streamErr.Epoch)
	assert.Less(t, streamErr.Revision, uint64(3+changes))
	assert.LessOrEqual(t, strings.Count(out, "event: UPSERT"), 1+1+bufferSize, "snapshot, the change in flight, the buffer")

	// It resumes from that cursor and gets the latest state only.
	resumed := watch(t, ts.URL, fmt.Sprintf("prefix=devices/&since=%d&epoch=%s", streamErr.Revision, epoch), nil)
	ev := resumed.next(t)
	require.Equal(t, httpsse.EventUpsert, ev.Event)
	var r struct {
		Revision uint64           `json:"revision"`
		Spec     model.DeviceSpec `json:"spec"`
	}
	require.NoError(t, json.Unmarshal([]byte(ev.Data), &r))
	assert.Equal(t, uint64(3+changes), r.Revision)
	assert.Equal(t, fmt.Sprintf("%d.0", 229-changes+1), r.Spec.Properties[0].Reported.Value)
	assert.Equal(t, httpsse.EventSynced, resumed.next(t).Event)
}
