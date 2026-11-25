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

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

var testTime = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

func newTestStore() *Store {
	return New(WithClock(func() time.Time { return testTime }))
}

func nodeResource(name string, category model.NodeCategory, ready, resourceVersion string) model.Resource {
	return model.Resource{
		Kind:   model.KindNode,
		Key:    model.NodeKey(category, name),
		Source: model.SourceRef{ResourceVersion: resourceVersion},
		Spec:   &model.NodeSpec{Name: name, Category: category, Ready: ready},
	}
}

func TestUpsertAssignsRevisionAndEnvelope(t *testing.T) {
	s := newTestStore()
	assert.Equal(t, uint64(0), s.Revision())

	changed, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1"))
	require.NoError(t, err)
	assert.True(t, changed)

	got, ok := s.Get("nodes/edge-worker/u2deb-edge")
	require.True(t, ok)
	assert.Equal(t, model.SchemaVersion, got.SchemaVersion)
	assert.Equal(t, uint64(1), got.Revision)
	assert.Equal(t, testTime, got.ObservedAt)
	assert.Equal(t, "1", got.Source.ResourceVersion)

	changed, err = s.Upsert(nodeResource("u2deb-cloud", model.NodeCategoryCloudControlPlane, "True", "2"))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, uint64(2), s.Revision())
}

func TestUpsertSuppressesUnchangedSpec(t *testing.T) {
	s := newTestStore()
	_, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1"))
	require.NoError(t, err)
	sub := s.Subscribe("", 0)
	defer sub.Close()

	// Same spec, new resourceVersion: no revision, no event, stored version kept.
	changed, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "2"))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, uint64(1), s.Revision())
	got, _ := s.Get("nodes/edge-worker/u2deb-edge")
	assert.Equal(t, "1", got.Source.ResourceVersion)
	assert.Empty(t, sub.Events())

	// Spec change: new revision and event.
	changed, err = s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "Unknown", "3"))
	require.NoError(t, err)
	assert.True(t, changed)
	ev := <-sub.Events()
	assert.Equal(t, EventUpsert, ev.Type)
	assert.Equal(t, uint64(2), ev.Revision)
	assert.Equal(t, "Unknown", ev.Resource.Spec.(*model.NodeSpec).Ready)
}

func TestUpsertRejectsInvalidResource(t *testing.T) {
	s := newTestStore()
	r := nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1")
	r.Key = ""
	_, err := s.Upsert(r)
	assert.Error(t, err)

	r = nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1")
	r.Spec = func() {} // not JSON-encodable
	_, err = s.Upsert(r)
	assert.Error(t, err)
	assert.Equal(t, uint64(0), s.Revision())
}

func TestDelete(t *testing.T) {
	s := newTestStore()
	_, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1"))
	require.NoError(t, err)
	sub := s.Subscribe("nodes/", 0)
	defer sub.Close()

	assert.False(t, s.Delete("nodes/edge-worker/unknown"))
	assert.Equal(t, uint64(1), s.Revision())

	assert.True(t, s.Delete("nodes/edge-worker/u2deb-edge"))
	assert.Equal(t, uint64(2), s.Revision())
	_, ok := s.Get("nodes/edge-worker/u2deb-edge")
	assert.False(t, ok)

	ev := <-sub.Events()
	assert.Equal(t, Event{Type: EventDelete, Key: "nodes/edge-worker/u2deb-edge", Revision: 2}, ev)
}

func TestListFiltersAndSorts(t *testing.T) {
	s := newTestStore()
	for _, r := range []model.Resource{
		nodeResource("worker-1", model.NodeCategoryCloudWorker, "True", "1"),
		nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1"),
		nodeResource("u2deb-cloud", model.NodeCategoryCloudControlPlane, "True", "1"),
		nodeResource("edge-2", model.NodeCategoryEdgeWorker, "True", "1"),
	} {
		_, err := s.Upsert(r)
		require.NoError(t, err)
	}

	all, rev := s.List("")
	assert.Equal(t, uint64(4), rev)
	require.Len(t, all, 4)
	assert.Equal(t, []string{
		"nodes/cloud-control-plane/u2deb-cloud",
		"nodes/cloud-worker/worker-1",
		"nodes/edge-worker/edge-2",
		"nodes/edge-worker/u2deb-edge",
	}, keys(all))

	edge, _ := s.List("nodes/edge-worker/")
	assert.Equal(t, []string{"nodes/edge-worker/edge-2", "nodes/edge-worker/u2deb-edge"}, keys(edge))

	none, _ := s.List("devices/")
	assert.Empty(t, none)
}

func TestSubscribeSnapshotThenFilteredEvents(t *testing.T) {
	s := newTestStore()
	_, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1"))
	require.NoError(t, err)
	_, err = s.Upsert(nodeResource("u2deb-cloud", model.NodeCategoryCloudControlPlane, "True", "1"))
	require.NoError(t, err)

	sub := s.Subscribe("nodes/edge-worker/", 0)
	defer sub.Close()
	assert.Equal(t, uint64(2), sub.Revision)
	assert.Equal(t, []string{"nodes/edge-worker/u2deb-edge"}, keys(sub.Snapshot))

	_, err = s.Upsert(nodeResource("u2deb-cloud", model.NodeCategoryCloudControlPlane, "False", "2"))
	require.NoError(t, err)
	_, err = s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "False", "2"))
	require.NoError(t, err)

	ev := <-sub.Events()
	assert.Equal(t, "nodes/edge-worker/u2deb-edge", ev.Key)
	assert.Equal(t, uint64(4), ev.Revision)
	assert.Empty(t, sub.Events(), "the control-plane change must not reach an edge-worker subscriber")
}

func TestSlowSubscriberIsClosedWithoutBlocking(t *testing.T) {
	s := newTestStore()
	slow := s.Subscribe("", 1)
	fast := s.Subscribe("", 10)
	defer fast.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, ready := range []string{"True", "False", "Unknown"} {
			_, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, ready, ""))
			assert.NoError(t, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Upsert blocked on a full subscriber buffer")
	}

	// The slow subscriber got the first event, then was closed with an overflow error.
	ev, ok := <-slow.Events()
	assert.True(t, ok)
	assert.Equal(t, uint64(1), ev.Revision)
	_, ok = <-slow.Events()
	assert.False(t, ok)
	assert.ErrorIs(t, slow.Err(), ErrSubscriptionOverflow)

	// The fast subscriber saw all three changes.
	for want := uint64(1); want <= 3; want++ {
		ev := <-fast.Events()
		assert.Equal(t, want, ev.Revision)
	}
	assert.NoError(t, fast.Err())
}

func TestCloseIsIdempotent(t *testing.T) {
	s := newTestStore()
	sub := s.Subscribe("", 0)
	sub.Close()
	sub.Close()
	_, ok := <-sub.Events()
	assert.False(t, ok)
	assert.NoError(t, sub.Err())

	// A closed subscription no longer receives events, and Upsert does not panic.
	_, err := s.Upsert(nodeResource("u2deb-edge", model.NodeCategoryEdgeWorker, "True", "1"))
	assert.NoError(t, err)
}

func TestEpoch(t *testing.T) {
	a, b := New(), New()
	assert.Len(t, a.Epoch(), 16)
	assert.NotEqual(t, a.Epoch(), b.Epoch(), "every store has its own epoch")
	assert.Equal(t, "e1", New(WithEpoch("e1")).Epoch())
}

func upsert(t *testing.T, s *Store, name string, category model.NodeCategory, ready string) {
	t.Helper()
	changed, err := s.Upsert(nodeResource(name, category, ready, ""))
	require.NoError(t, err)
	require.True(t, changed)
}

type change struct {
	Type     EventType
	Key      string
	Revision uint64
}

func changes(events []Event) []change {
	out := make([]change, 0, len(events))
	for _, ev := range events {
		out = append(out, change{ev.Type, ev.Key, ev.Revision})
	}
	return out
}

func TestSubscribeSinceReturnsDelta(t *testing.T) {
	const (
		edge  = "nodes/edge-worker/u2deb-edge"
		cloud = "nodes/cloud-control-plane/u2deb-cloud"
		w1    = "nodes/cloud-worker/w1"
		w2    = "nodes/cloud-worker/w2"
	)
	s := newTestStore()
	upsert(t, s, "u2deb-edge", model.NodeCategoryEdgeWorker, "True")          // 1
	upsert(t, s, "u2deb-cloud", model.NodeCategoryCloudControlPlane, "True")  // 2
	upsert(t, s, "w1", model.NodeCategoryCloudWorker, "True")                 // 3
	upsert(t, s, "w2", model.NodeCategoryCloudWorker, "True")                 // 4
	upsert(t, s, "u2deb-edge", model.NodeCategoryEdgeWorker, "Unknown")       // 5
	require.True(t, s.Delete(w1))                                             // 6
	upsert(t, s, "u2deb-cloud", model.NodeCategoryCloudControlPlane, "False") // 7
	require.True(t, s.Delete(w2))                                             // 8
	upsert(t, s, "w2", model.NodeCategoryCloudWorker, "False")                // 9: supersedes delete 8

	sub := s.SubscribeSince("", 4, 0)
	defer sub.Close()
	require.True(t, sub.Resumed)
	assert.Nil(t, sub.Snapshot)
	assert.Equal(t, uint64(9), sub.Revision)
	assert.Equal(t, []change{
		{EventUpsert, edge, 5},
		{EventDelete, w1, 6},
		{EventUpsert, cloud, 7},
		{EventUpsert, w2, 9},
	}, changes(sub.Delta))
	assert.Equal(t, "Unknown", sub.Delta[0].Resource.Spec.(*model.NodeSpec).Ready)

	// The delta is filtered by prefix.
	workers := s.SubscribeSince("nodes/cloud-worker/", 4, 0)
	defer workers.Close()
	require.True(t, workers.Resumed)
	assert.Equal(t, []change{{EventDelete, w1, 6}, {EventUpsert, w2, 9}}, changes(workers.Delta))

	// Up to date: an empty delta, then live events.
	current := s.SubscribeSince("", 9, 0)
	defer current.Close()
	require.True(t, current.Resumed)
	assert.Empty(t, current.Delta)
	require.True(t, s.Delete(edge))
	ev := <-current.Events()
	assert.Equal(t, change{EventDelete, edge, 10}, change{ev.Type, ev.Key, ev.Revision})
}

func TestSubscribeSinceFallsBackToSnapshot(t *testing.T) {
	s := New(WithDeleteHistory(2))
	for _, n := range []string{"a", "b", "c", "d"} {
		upsert(t, s, n, model.NodeCategoryCloudWorker, "True") // 1-4
	}

	// Ahead of the store, for example after a publisher restart.
	ahead := s.SubscribeSince("", 5, 0)
	defer ahead.Close()
	assert.False(t, ahead.Resumed)
	assert.Len(t, ahead.Snapshot, 4)

	require.True(t, s.Delete("nodes/cloud-worker/a")) // 5
	require.True(t, s.Delete("nodes/cloud-worker/b")) // 6
	resumed := s.SubscribeSince("", 4, 0)
	defer resumed.Close()
	assert.True(t, resumed.Resumed, "both deletes are remembered")

	// A third delete forgets the oldest (revision 5): resuming from before it is not possible.
	require.True(t, s.Delete("nodes/cloud-worker/c")) // 7
	tooOld := s.SubscribeSince("", 4, 0)
	defer tooOld.Close()
	assert.False(t, tooOld.Resumed)
	assert.Equal(t, []string{"nodes/cloud-worker/d"}, keys(tooOld.Snapshot))

	fromForgotten := s.SubscribeSince("", 5, 0)
	defer fromForgotten.Close()
	require.True(t, fromForgotten.Resumed)
	assert.Equal(t, []change{
		{EventDelete, "nodes/cloud-worker/b", 6},
		{EventDelete, "nodes/cloud-worker/c", 7},
	}, changes(fromForgotten.Delta))
}

func TestSubscribeSinceWithoutDeleteHistory(t *testing.T) {
	s := New(WithDeleteHistory(0))
	upsert(t, s, "a", model.NodeCategoryCloudWorker, "True") // 1
	upsert(t, s, "b", model.NodeCategoryCloudWorker, "True") // 2

	sub := s.SubscribeSince("", 1, 0)
	defer sub.Close()
	assert.True(t, sub.Resumed, "nothing deleted since revision 1")

	require.True(t, s.Delete("nodes/cloud-worker/a")) // 3
	after := s.SubscribeSince("", 2, 0)
	defer after.Close()
	assert.False(t, after.Resumed)
}

func keys(resources []model.Resource) []string {
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		out = append(out, r.Key)
	}
	return out
}
