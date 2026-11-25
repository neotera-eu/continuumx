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

package httpsse

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
)

// resumePoint is where a subscriber asks to resume, if anywhere.
type resumePoint struct {
	cursor Cursor
	ok     bool
}

// parseResumePoint reads ParamSince and ParamEpoch, or else the Last-Event-ID header. An
// invalid ParamSince is a client error. An invalid Last-Event-ID, which a browser resends on
// its own, only means a fresh start.
func parseResumePoint(r *http.Request) (resumePoint, error) {
	q := r.URL.Query()
	if q.Has(ParamSince) {
		since, err := strconv.ParseUint(q.Get(ParamSince), 10, 64)
		if err != nil {
			return resumePoint{}, fmt.Errorf("invalid %s: %w", ParamSince, err)
		}
		return resumePoint{cursor: Cursor{Epoch: q.Get(ParamEpoch), Revision: since}, ok: true}, nil
	}
	if id := r.Header.Get(HeaderLastEventID); id != "" {
		c, err := ParseEventID(id)
		if err != nil {
			klog.V(2).Infof("dtpublisher: ignore %s: %v", HeaderLastEventID, err)
			return resumePoint{}, nil
		}
		return resumePoint{cursor: c, ok: true}, nil
	}
	return resumePoint{}, nil
}

func (s *Server) serveWatch(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get(ParamPrefix)
	if strings.ContainsAny(prefix, "*?#$") {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "%s must not contain wildcard characters", ParamPrefix)
		return
	}
	resume, err := parseResumePoint(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "%v", err)
		return
	}
	// The prefix is fixed for the life of the stream, so one check covers every event.
	if !s.authorize(w, r, prefix) {
		return
	}

	// A resume point of another epoch, for example from before a publisher restart, gets a
	// fresh start: its revisions mean nothing here.
	epoch := s.store.Epoch()
	var sub *store.Subscription
	if resume.ok && resume.cursor.Epoch == epoch {
		sub = s.store.SubscribeSince(prefix, resume.cursor.Revision, s.opts.BufferSize)
	} else {
		sub = s.store.Subscribe(prefix, s.opts.BufferSize)
	}
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), timeout: s.opts.WriteTimeout}

	klog.V(2).Infof("dtpublisher: subscriber %s (%s) prefix=%q resumed=%t revision=%d", r.RemoteAddr, security.Identity(r.TLS), prefix, sub.Resumed, sub.Revision)
	defer klog.V(2).Infof("dtpublisher: subscriber %s gone", r.RemoteAddr)

	if err := s.writeInitial(sw, sub); err != nil {
		klog.V(2).Infof("dtpublisher: subscriber %s: %v", r.RemoteAddr, err)
		return
	}

	last := Cursor{Epoch: epoch, Revision: sub.Revision}
	heartbeat := time.NewTicker(s.opts.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			err = sw.send(EventHeartbeat, EventID(last), last)
		case ev, ok := <-sub.Events():
			if !ok {
				if subErr := sub.Err(); errors.Is(subErr, store.ErrSubscriptionOverflow) {
					klog.Warningf("dtpublisher: subscriber %s fell behind; stream closed", r.RemoteAddr)
					_ = sw.send(EventError, "", StreamError{Reason: ReasonOverflow, Message: subErr.Error(), Cursor: last})
				}
				return
			}
			last.Revision = ev.Revision
			err = sendChange(sw, ev, EventID(last))
		}
		if err != nil {
			klog.V(2).Infof("dtpublisher: subscriber %s: %v", r.RemoteAddr, err)
			return
		}
	}
}

// writeInitial brings the subscriber to sub.Revision and sends SYNCED. Initial events carry
// no ID: a subscriber that stops halfway does not hold a consistent state to resume from.
func (s *Server) writeInitial(sw *sseWriter, sub *store.Subscription) error {
	cursor := Cursor{Epoch: s.store.Epoch(), Revision: sub.Revision}
	if sub.Resumed {
		for _, ev := range sub.Delta {
			if err := sendChange(sw, ev, ""); err != nil {
				return err
			}
		}
	} else {
		if err := sw.send(EventReset, "", cursor); err != nil {
			return err
		}
		for i := range sub.Snapshot {
			if err := sw.send(EventUpsert, "", &sub.Snapshot[i]); err != nil {
				return err
			}
		}
	}
	return sw.send(EventSynced, EventID(cursor), cursor)
}

func sendChange(sw *sseWriter, ev store.Event, id string) error {
	if ev.Type == store.EventDelete {
		return sw.send(EventDelete, id, Deletion{Key: ev.Key, Revision: ev.Revision})
	}
	return sw.send(EventUpsert, id, ev.Resource)
}

// sseWriter writes Server-Sent Events, one flush per event, each write bounded by timeout.
type sseWriter struct {
	w       io.Writer
	rc      *http.ResponseController
	timeout time.Duration
}

func (sw *sseWriter) send(event, id string, data interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", event, err)
	}
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteByte('\n')
	if id != "" {
		b.WriteString("id: ")
		b.WriteString(id)
		b.WriteByte('\n')
	}
	// json.Marshal output has no newline, so it is a single data line.
	b.WriteString("data: ")
	b.Write(payload)
	b.WriteString("\n\n")

	if err := sw.rc.SetWriteDeadline(time.Now().Add(sw.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := io.WriteString(sw.w, b.String()); err != nil {
		return fmt.Errorf("write %s event: %w", event, err)
	}
	if err := sw.rc.Flush(); err != nil {
		return fmt.Errorf("flush %s event: %w", event, err)
	}
	return nil
}
