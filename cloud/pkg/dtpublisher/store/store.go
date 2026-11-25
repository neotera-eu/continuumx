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

// Package store holds the current published state of the Device Twins Resources Publisher.
//
// Every accepted change gets the next revision and is fanned out to the subscribers whose
// prefix selects its key. An update whose spec is unchanged is suppressed: it neither takes
// a revision nor produces an event. Fan-out never blocks: a subscriber whose buffer is full
// is closed with ErrSubscriptionOverflow and must subscribe again.
//
// Revisions count from 1 in each store, so they are only comparable within one Epoch, a random
// identifier chosen when the store is created. The store keeps a bounded history of deletes,
// so a subscriber that knows an epoch and a revision can resume with only the changes since
// then (SubscribeSince).
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

const (
	// DefaultBufferSize is the event buffer of a subscription when none is given.
	DefaultBufferSize = 256
	// DefaultDeleteHistory is the number of deletes the store remembers for SubscribeSince.
	DefaultDeleteHistory = 1024
)

// ErrSubscriptionOverflow is reported by a subscription closed because its buffer was full.
var ErrSubscriptionOverflow = errors.New("subscription closed: event buffer overflow")

// EventType is the type of a change event.
type EventType string

const (
	// EventUpsert means a resource was added or changed.
	EventUpsert EventType = "UPSERT"
	// EventDelete means a resource was removed.
	EventDelete EventType = "DELETE"
)

// Event is a change of the published state.
type Event struct {
	// Type is the change type.
	Type EventType
	// Key is the key of the changed resource.
	Key string
	// Revision is the revision of the change.
	Revision uint64
	// Resource is the new resource for EventUpsert, nil for EventDelete.
	Resource *model.Resource
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock used for Resource.ObservedAt.
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		s.now = now
	}
}

// WithEpoch sets the epoch instead of a random one. For tests.
func WithEpoch(epoch string) Option {
	return func(s *Store) {
		s.epoch = epoch
	}
}

// WithDeleteHistory sets how many deletes the store remembers for SubscribeSince. A value of
// zero or less remembers none, so a subscriber can resume only if nothing was deleted since.
func WithDeleteHistory(n int) Option {
	return func(s *Store) {
		s.deleteHistory = n
	}
}

type entry struct {
	resource    model.Resource
	fingerprint []byte
}

// Store is the current published state. It is safe for concurrent use.
type Store struct {
	now           func() time.Time
	epoch         string
	deleteHistory int

	mu       sync.Mutex
	revision uint64
	entries  map[string]entry
	subs     map[*Subscription]struct{}
	// deleted maps the key of each remembered delete to its revision. A key added again is
	// removed from it: the upsert supersedes the delete.
	deleted map[string]uint64
	// forgotten is the highest revision of a delete no longer remembered. A subscriber can
	// resume only from a revision at or above it.
	forgotten uint64
	// counters for Stats
	upserts, deletes, overflows uint64
}

// Stats are counters of a store, for metrics.
type Stats struct {
	// Revision is the current revision.
	Revision uint64
	// Resources counts the published resources per kind.
	Resources map[model.Kind]int
	// Subscribers is the number of open subscriptions.
	Subscribers int
	// Upserts and Deletes count the accepted changes since the store was created.
	Upserts, Deletes uint64
	// Overflows counts the subscriptions closed with ErrSubscriptionOverflow.
	Overflows uint64
}

// Stats returns the current counters.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := Stats{
		Revision:    s.revision,
		Resources:   make(map[model.Kind]int),
		Subscribers: len(s.subs),
		Upserts:     s.upserts,
		Deletes:     s.deletes,
		Overflows:   s.overflows,
	}
	for _, e := range s.entries {
		st.Resources[e.resource.Kind]++
	}
	return st
}

// New creates an empty Store.
func New(opts ...Option) *Store {
	s := &Store{
		now:           time.Now,
		deleteHistory: DefaultDeleteHistory,
		entries:       make(map[string]entry),
		subs:          make(map[*Subscription]struct{}),
		deleted:       make(map[string]uint64),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.epoch == "" {
		s.epoch = newEpoch()
	}
	return s
}

func newEpoch() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to the start time.
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Epoch identifies this store instance. Revisions of different epochs are not comparable.
func (s *Store) Epoch() string {
	return s.epoch
}

// Upsert adds or replaces the resource under r.Key. It reports whether the published state
// changed: an unchanged Kind and Spec is a no-op, whatever the source resourceVersion.
func (s *Store) Upsert(r model.Resource) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	fingerprint, err := json.Marshal(r.Spec)
	if err != nil {
		return false, fmt.Errorf("encode spec of %q: %w", r.Key, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if old, ok := s.entries[r.Key]; ok && old.resource.Kind == r.Kind && bytes.Equal(old.fingerprint, fingerprint) {
		return false, nil
	}
	s.revision++
	s.upserts++
	r.SchemaVersion = model.SchemaVersion
	r.Revision = s.revision
	r.ObservedAt = s.now().UTC()
	s.entries[r.Key] = entry{resource: r, fingerprint: fingerprint}
	delete(s.deleted, r.Key)

	published := r
	s.broadcast(Event{Type: EventUpsert, Key: r.Key, Revision: r.Revision, Resource: &published})
	return true, nil
}

// Delete removes the resource under key. It reports whether a resource was removed.
func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[key]; !ok {
		return false
	}
	delete(s.entries, key)
	s.revision++
	s.deletes++
	s.rememberDeleteLocked(key, s.revision)
	s.broadcast(Event{Type: EventDelete, Key: key, Revision: s.revision})
	return true
}

// rememberDeleteLocked must be called with s.mu held.
func (s *Store) rememberDeleteLocked(key string, revision uint64) {
	if s.deleteHistory <= 0 {
		s.forgotten = revision
		return
	}
	s.deleted[key] = revision
	if len(s.deleted) <= s.deleteHistory {
		return
	}
	oldestKey, oldest := "", uint64(0)
	for k, r := range s.deleted {
		if oldestKey == "" || r < oldest {
			oldestKey, oldest = k, r
		}
	}
	delete(s.deleted, oldestKey)
	if oldest > s.forgotten {
		s.forgotten = oldest
	}
}

// Get returns the resource under key.
func (s *Store) Get(key string) (model.Resource, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[key]
	return e.resource, ok
}

// List returns the resources whose keys match prefix, sorted by key, and the current revision.
func (s *Store) List(prefix string) ([]model.Resource, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.listLocked(prefix), s.revision
}

// Revision returns the revision of the last accepted change, 0 if none.
func (s *Store) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.revision
}

// Subscribe returns a subscription to the changes of the keys matching prefix. Its Snapshot
// is the matching state at Revision; Events delivers every later change, in revision order.
// A bufferSize of zero or less uses DefaultBufferSize.
func (s *Store) Subscribe(prefix string, bufferSize int) *Subscription {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sub := s.subscribeLocked(prefix, bufferSize)
	sub.Snapshot = s.listLocked(prefix)
	return sub
}

// SubscribeSince is Subscribe for a subscriber that already holds the state of prefix at
// revision since of this store's epoch. If the store can tell what changed since then, the
// subscription has Resumed set and Delta holds those changes instead of a Snapshot: one
// UPSERT per matching resource changed after since, with its current value, and one DELETE
// per matching key deleted after since, in revision order. Otherwise, because since is ahead
// of the store or deletes after since are no longer remembered, it is a plain Subscribe.
// The caller checks the epoch.
func (s *Store) SubscribeSince(prefix string, since uint64, bufferSize int) *Subscription {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sub := s.subscribeLocked(prefix, bufferSize)
	if since > s.revision || since < s.forgotten {
		sub.Snapshot = s.listLocked(prefix)
		return sub
	}
	sub.Resumed = true
	sub.Delta = []Event{}
	for key, e := range s.entries {
		if e.resource.Revision > since && model.MatchesPrefix(key, prefix) {
			r := e.resource
			sub.Delta = append(sub.Delta, Event{Type: EventUpsert, Key: key, Revision: r.Revision, Resource: &r})
		}
	}
	for key, revision := range s.deleted {
		if revision > since && model.MatchesPrefix(key, prefix) {
			sub.Delta = append(sub.Delta, Event{Type: EventDelete, Key: key, Revision: revision})
		}
	}
	sort.Slice(sub.Delta, func(i, j int) bool { return sub.Delta[i].Revision < sub.Delta[j].Revision })
	return sub
}

// subscribeLocked must be called with s.mu held.
func (s *Store) subscribeLocked(prefix string, bufferSize int) *Subscription {
	sub := &Subscription{
		Revision: s.revision,
		store:    s,
		prefix:   prefix,
		events:   make(chan Event, bufferSize),
	}
	s.subs[sub] = struct{}{}
	return sub
}

func (s *Store) listLocked(prefix string) []model.Resource {
	out := make([]model.Resource, 0, len(s.entries))
	for key, e := range s.entries {
		if model.MatchesPrefix(key, prefix) {
			out = append(out, e.resource)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// broadcast must be called with s.mu held.
func (s *Store) broadcast(ev Event) {
	for sub := range s.subs {
		if !model.MatchesPrefix(ev.Key, sub.prefix) {
			continue
		}
		select {
		case sub.events <- ev:
		default:
			s.overflows++
			s.closeLocked(sub, ErrSubscriptionOverflow)
		}
	}
}

// closeLocked must be called with s.mu held.
func (s *Store) closeLocked(sub *Subscription, err error) {
	if _, ok := s.subs[sub]; !ok {
		return
	}
	delete(s.subs, sub)
	sub.err = err
	close(sub.events)
}

// Subscription is a subscriber's view of the store.
type Subscription struct {
	// Snapshot is the matching state when the subscription was created, sorted by key.
	// It is nil if Resumed is set.
	Snapshot []model.Resource
	// Resumed is set by SubscribeSince when Delta, not Snapshot, brings the subscriber to
	// Revision.
	Resumed bool
	// Delta holds the changes since the revision given to SubscribeSince, in revision order,
	// if Resumed is set.
	Delta []Event
	// Revision is the store revision of Snapshot, or the revision Delta brings the subscriber to.
	Revision uint64

	store  *Store
	prefix string
	events chan Event
	err    error
}

// Events delivers the changes after Revision. It is closed by Close or on overflow.
func (sub *Subscription) Events() <-chan Event {
	return sub.events
}

// Err returns ErrSubscriptionOverflow if the subscription was closed because its buffer was
// full, and nil otherwise. Call it after Events is closed.
func (sub *Subscription) Err() error {
	sub.store.mu.Lock()
	defer sub.store.mu.Unlock()

	return sub.err
}

// Close ends the subscription. It is safe to call more than once.
func (sub *Subscription) Close() {
	sub.store.mu.Lock()
	defer sub.store.mu.Unlock()

	sub.store.closeLocked(sub, nil)
}
