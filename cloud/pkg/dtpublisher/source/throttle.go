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

package source

import (
	"sync"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

// Throttle is a Sink that passes at most one change per key per minimum interval to the next
// Sink. A change that arrives too early is held back; a later change replaces it, and the
// latest one is passed on when the interval ends, so the last value is never lost. A delete is
// passed on at once and drops any held change. Only changes the next Sink accepts as changes
// start an interval; no-ops do not.
type Throttle struct {
	next        Sink
	minInterval time.Duration
	clock       clock.WithDelayedExecution

	mu      sync.Mutex
	keys    map[string]*throttleKey
	stopped bool
}

type throttleKey struct {
	lastChange time.Time
	pending    *model.Resource
	timer      clock.Timer
}

var _ Sink = (*Throttle)(nil)

// NewThrottle wraps next. A minInterval of zero or less passes every change on at once.
// A nil clk uses the real clock.
func NewThrottle(next Sink, minInterval time.Duration, clk clock.WithDelayedExecution) *Throttle {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &Throttle{
		next:        next,
		minInterval: minInterval,
		clock:       clk,
		keys:        make(map[string]*throttleKey),
	}
}

// Upsert passes r on, or holds it until the interval of its key ends. It reports whether the
// next Sink accepted r as a change now; a held resource reports false. An invalid resource is
// rejected at once.
func (t *Throttle) Upsert(r model.Resource) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	if t.minInterval <= 0 {
		return t.next.Upsert(r)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return false, nil
	}
	k := t.keys[r.Key]
	if k == nil {
		k = &throttleKey{}
		t.keys[r.Key] = k
	}
	now := t.clock.Now()
	if k.timer == nil && (k.lastChange.IsZero() || now.Sub(k.lastChange) >= t.minInterval) {
		return t.passLocked(k, r, now)
	}

	held := r
	k.pending = &held
	if k.timer == nil {
		key, due := r.Key, k.lastChange.Add(t.minInterval)
		k.timer = t.clock.AfterFunc(due.Sub(now), func() { t.flush(key, k, due) })
	}
	return false, nil
}

// Delete drops any held change of key and passes the delete on.
func (t *Throttle) Delete(key string) bool {
	t.mu.Lock()
	if k, ok := t.keys[key]; ok {
		if k.timer != nil {
			k.timer.Stop()
		}
		delete(t.keys, key)
	}
	t.mu.Unlock()

	return t.next.Delete(key)
}

// Stop drops every held change. Later calls to Upsert are ignored; Delete still passes on.
func (t *Throttle) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stopped = true
	for key, k := range t.keys {
		if k.timer != nil {
			k.timer.Stop()
		}
		delete(t.keys, key)
	}
}

// flush passes on the held change of key at due, the end of its interval. owner is the entry
// the timer was started for; a timer that fires after its key was deleted (and possibly added
// again) does nothing. flush does not read the clock, so it may run inside a clock callback.
func (t *Throttle) flush(key string, owner *throttleKey, due time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	k, ok := t.keys[key]
	if !ok || k != owner || t.stopped {
		return
	}
	k.timer = nil
	if k.pending == nil {
		return
	}
	r := *k.pending
	k.pending = nil
	if _, err := t.passLocked(k, r, due); err != nil {
		klog.Errorf("dtpublisher: publish held change of %s: %v", key, err)
	}
}

// passLocked must be called with t.mu held.
func (t *Throttle) passLocked(k *throttleKey, r model.Resource, now time.Time) (bool, error) {
	changed, err := t.next.Upsert(r)
	if changed {
		k.lastChange = now
	}
	return changed, err
}
