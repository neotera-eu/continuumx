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

func keys(resources []model.Resource) []string {
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		out = append(out, r.Key)
	}
	return out
}
