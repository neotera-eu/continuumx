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

package node

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
)

const (
	waitTimeout  = 5 * time.Second
	pollInterval = 10 * time.Millisecond
)

// startSource runs a node source on a fake clientset and returns the client and the store.
func startSource(t *testing.T, cfg Config, initial ...*corev1.Node) (*fake.Clientset, *store.Store) {
	t.Helper()
	client := fake.NewSimpleClientset()
	for _, n := range initial {
		_, err := client.CoreV1().Nodes().Create(context.Background(), n, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	factory := informers.NewSharedInformerFactory(client, 0)
	src := New(factory.Core().V1().Nodes().Informer(), cfg)
	st := store.New()

	errCh := make(chan error, 1)
	go func() { errCh <- src.Run(ctx, st) }()
	factory.Start(ctx.Done())

	t.Cleanup(func() {
		cancel()
		factory.Shutdown()
		select {
		case err := <-errCh:
			assert.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Error("node source did not stop")
		}
	})
	return client, st
}

func waitForKeys(t *testing.T, st *store.Store, prefix string, want ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		got, _ := st.List(prefix)
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i].Key != want[i] {
				return false
			}
		}
		return true
	}, waitTimeout, pollInterval, "keys under %q never became %v", prefix, want)
}

func TestSourcePublishesInitialNodes(t *testing.T) {
	edge := loadEdgeNode(t)
	cloud := nodeWithLabels("u2deb-cloud", map[string]string{ControlPlaneRoleLabel: ""})
	worker := nodeWithLabels("worker-1", nil)

	_, st := startSource(t, Config{}, edge, cloud, worker)

	waitForKeys(t, st, "",
		"nodes/cloud-control-plane/u2deb-cloud",
		"nodes/cloud-worker/worker-1",
		"nodes/edge-worker/u2deb-edge",
	)
	got, ok := st.Get("nodes/edge-worker/u2deb-edge")
	require.True(t, ok)
	assert.Equal(t, "True", got.Spec.(*model.NodeSpec).Ready)
}

func TestSourceFollowsUpdatesAndDeletes(t *testing.T) {
	ctx := context.Background()
	client, st := startSource(t, Config{}, nodeWithLabels("edge-2", nil))
	waitForKeys(t, st, "", "nodes/cloud-worker/edge-2")

	// Joining as an edge node moves the node to the edge-worker key.
	n, err := client.CoreV1().Nodes().Get(ctx, "edge-2", metav1.GetOptions{})
	require.NoError(t, err)
	n.Labels = map[string]string{"node-role.kubernetes.io/edge": ""}
	_, err = client.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
	require.NoError(t, err)
	waitForKeys(t, st, "", "nodes/edge-worker/edge-2")

	// A status change is published.
	n, err = client.CoreV1().Nodes().Get(ctx, "edge-2", metav1.GetOptions{})
	require.NoError(t, err)
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	_, err = client.CoreV1().Nodes().UpdateStatus(ctx, n, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r, ok := st.Get("nodes/edge-worker/edge-2")
		return ok && r.Spec.(*model.NodeSpec).Ready == "True"
	}, waitTimeout, pollInterval)

	require.NoError(t, client.CoreV1().Nodes().Delete(ctx, "edge-2", metav1.DeleteOptions{}))
	waitForKeys(t, st, "")
}

func TestSourceSubscriberSeesCategoryMoveAsDeleteThenUpsert(t *testing.T) {
	ctx := context.Background()
	client, st := startSource(t, Config{}, nodeWithLabels("edge-3", nil))
	waitForKeys(t, st, "", "nodes/cloud-worker/edge-3")

	sub := st.Subscribe("nodes/", 0)
	defer sub.Close()

	n, err := client.CoreV1().Nodes().Get(ctx, "edge-3", metav1.GetOptions{})
	require.NoError(t, err)
	n.Labels = map[string]string{"node-role.kubernetes.io/edge": ""}
	_, err = client.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
	require.NoError(t, err)

	first := receive(t, sub)
	assert.Equal(t, store.EventDelete, first.Type)
	assert.Equal(t, "nodes/cloud-worker/edge-3", first.Key)
	second := receive(t, sub)
	assert.Equal(t, store.EventUpsert, second.Type)
	assert.Equal(t, "nodes/edge-worker/edge-3", second.Key)
	assert.Greater(t, second.Revision, first.Revision)
}

func receive(t *testing.T, sub *store.Subscription) store.Event {
	t.Helper()
	select {
	case ev, ok := <-sub.Events():
		require.True(t, ok, "subscription closed: %v", sub.Err())
		return ev
	case <-time.After(waitTimeout):
		t.Fatal("no event received")
		return store.Event{}
	}
}
