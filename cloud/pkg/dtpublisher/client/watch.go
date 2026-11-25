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

package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"strings"
	"sync"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

// MaxEventSize bounds one line of the event stream, and so one published resource.
const MaxEventSize = 16 * 1024 * 1024

// ErrStreamEnded is the Err of a watch the publisher ended without an ERROR event, for
// example because it shut down.
var ErrStreamEnded = errors.New("publisher ended the stream")

// StreamClosedError is the Err of a watch the publisher ended with an ERROR event.
type StreamClosedError struct {
	httpsse.StreamError
}

func (e *StreamClosedError) Error() string {
	return fmt.Sprintf("publisher closed the stream (%s): %s", e.Reason, e.Message)
}

// WatchOptions configure Watch.
type WatchOptions struct {
	// Prefix selects the keys; empty selects every key.
	Prefix string
	// Since resumes from a cursor, for example Watcher.Cursor of an ended watch or the Cursor
	// of a List. Nil starts fresh.
	Since *httpsse.Cursor
}

// Event is one event of a watch. Type is one of the httpsse Event constants; exactly one of
// the pointer fields is set for a known type.
type Event struct {
	// Type is the event name.
	Type string
	// ID is the event ID, set only on events after which the subscriber holds a consistent
	// state. See httpsse.EventID.
	ID string
	// Resource is set for UPSERT.
	Resource *model.Resource
	// Deletion is set for DELETE.
	Deletion *httpsse.Deletion
	// Cursor is set for RESET, SYNCED and HEARTBEAT.
	Cursor *httpsse.Cursor
	// Error is set for ERROR. The stream ends after it.
	Error *httpsse.StreamError
	// Data is the raw event data, kept for event types this client does not know.
	Data json.RawMessage
}

// Watcher is an open subscription.
type Watcher struct {
	events chan Event
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	err       error
	cursor    httpsse.Cursor
	hasCursor bool
}

// Watch subscribes to the standing data flow. It returns once the publisher has accepted the
// subscription; events then arrive on Watcher.Events until the stream ends.
func (c *Client) Watch(ctx context.Context, opts WatchOptions) (*Watcher, error) {
	q := url.Values{}
	if opts.Prefix != "" {
		q.Set(httpsse.ParamPrefix, opts.Prefix)
	}
	if opts.Since != nil {
		q.Set(httpsse.ParamSince, fmt.Sprint(opts.Since.Revision))
		q.Set(httpsse.ParamEpoch, opts.Since.Epoch)
	}

	ctx, cancel := context.WithCancel(ctx)
	resp, err := c.do(ctx, httpsse.WatchPath, q, "text/event-stream")
	if err != nil {
		cancel()
		return nil, err
	}
	if mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mediaType != "text/event-stream" {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("watch: publisher answered with %q, not an event stream", resp.Header.Get("Content-Type"))
	}

	w := &Watcher{events: make(chan Event), cancel: cancel, done: make(chan struct{})}
	if opts.Since != nil {
		w.cursor, w.hasCursor = *opts.Since, true
	}
	go w.read(ctx, resp.Body)
	return w, nil
}

// Events delivers the events in stream order. It is closed when the stream ends; Err then
// tells why.
func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Err returns why the stream ended, after Events is closed: nil after Close or when the
// context is done, a *StreamClosedError after an ERROR event, ErrStreamEnded when the
// publisher ended the stream otherwise, or the read or decode error.
func (w *Watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Cursor returns the cursor of the last delivered event that carried an ID, or the
// WatchOptions.Since cursor before that: the point to resume from with WatchOptions.Since.
// ok is false if there is none. It is exact once Events is closed; while the watch runs it may
// already include the event being handed over, so use Event.ID to track each event.
func (w *Watcher) Cursor() (cursor httpsse.Cursor, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cursor, w.hasCursor
}

// Close ends the subscription and waits until Events is closed.
func (w *Watcher) Close() {
	w.cancel()
	<-w.done
}

func (w *Watcher) read(ctx context.Context, body io.ReadCloser) {
	defer close(w.done)
	defer close(w.events)
	defer body.Close()

	err := parseStream(body, func(ev Event) bool {
		// The cursor moves with the event and moves back if the event is not delivered, so
		// once the stream has ended it is exactly the last delivered position.
		w.mu.Lock()
		prev, prevOK := w.cursor, w.hasCursor
		if c, err := httpsse.ParseEventID(ev.ID); ev.ID != "" && err == nil {
			w.cursor, w.hasCursor = c, true
		}
		w.mu.Unlock()

		select {
		case w.events <- ev:
			return ev.Type != httpsse.EventError
		case <-ctx.Done():
			w.mu.Lock()
			w.cursor, w.hasCursor = prev, prevOK
			w.mu.Unlock()
			return false
		}
	})

	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case ctx.Err() != nil:
		w.err = nil
	case err != nil:
		w.err = err
	default:
		w.err = ErrStreamEnded
	}
}

// parseStream reads Server-Sent Events from r and calls emit for each until emit returns
// false or the stream ends. It returns a *StreamClosedError after an ERROR event, nil at the
// end of the stream, or the read or decode error.
//
// It follows the SSE format: fields "event", "data" (repeatable, joined with newlines) and
// "id"; lines starting with ":" are comments; a blank line ends an event; "\r\n" line ends
// are accepted. Unlike a browser, ID only holds the ID set in the event itself.
func parseStream(r io.Reader, emit func(Event) bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEventSize)

	var (
		eventType, id string
		data          strings.Builder
		hasData       bool
	)
	reset := func() {
		eventType, id, hasData = "", "", false
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !hasData {
				reset()
				continue
			}
			ev, err := decodeEvent(eventType, id, data.String())
			reset()
			if err != nil {
				return err
			}
			if !emit(ev) {
				if ev.Error != nil {
					return &StreamClosedError{StreamError: *ev.Error}
				}
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			eventType = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		case "id":
			if !strings.ContainsRune(value, 0) {
				id = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read event stream: %w", err)
	}
	return nil
}

func decodeEvent(eventType, id, data string) (Event, error) {
	if eventType == "" {
		eventType = "message"
	}
	ev := Event{Type: eventType, ID: id, Data: json.RawMessage(data)}
	var target interface{}
	switch eventType {
	case httpsse.EventUpsert:
		r, err := DecodeResource([]byte(data))
		if err != nil {
			return Event{}, fmt.Errorf("%s event: %w", eventType, err)
		}
		ev.Resource = &r
		return ev, nil
	case httpsse.EventDelete:
		ev.Deletion = &httpsse.Deletion{}
		target = ev.Deletion
	case httpsse.EventReset, httpsse.EventSynced, httpsse.EventHeartbeat:
		ev.Cursor = &httpsse.Cursor{}
		target = ev.Cursor
	case httpsse.EventError:
		ev.Error = &httpsse.StreamError{}
		target = ev.Error
	default:
		return ev, nil
	}
	if err := json.Unmarshal([]byte(data), target); err != nil {
		return Event{}, fmt.Errorf("decode %s event: %w", eventType, err)
	}
	return ev, nil
}
