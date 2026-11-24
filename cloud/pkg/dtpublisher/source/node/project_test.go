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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

// loadEdgeNode loads the owner's reference edge node, u2deb-edge.
func loadEdgeNode(t *testing.T) *corev1.Node {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "u2deb-edge.yaml"))
	require.NoError(t, err)
	node := &corev1.Node{}
	require.NoError(t, yaml.Unmarshal(data, node))
	return node
}

func nodeWithLabels(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   model.NodeCategory
	}{
		{
			name:   "edge node as joined by cxadm",
			labels: map[string]string{"node-role.kubernetes.io/edge": "", "node-role.kubernetes.io/agent": ""},
			want:   model.NodeCategoryEdgeWorker,
		},
		{
			name:   "kubeadm control plane",
			labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
			want:   model.NodeCategoryCloudControlPlane,
		},
		{
			name:   "cloud worker without role labels",
			labels: map[string]string{"kubernetes.io/hostname": "worker-1"},
			want:   model.NodeCategoryCloudWorker,
		},
		{
			name:   "no labels",
			labels: nil,
			want:   model.NodeCategoryCloudWorker,
		},
		{
			name:   "edge label wins over control-plane label",
			labels: map[string]string{"node-role.kubernetes.io/edge": "", "node-role.kubernetes.io/control-plane": ""},
			want:   model.NodeCategoryEdgeWorker,
		},
		{
			// The repo's edge rule (nodes.IsEdgeNode) requires the empty value.
			name:   "edge label with a non-empty value is not an edge node",
			labels: map[string]string{"node-role.kubernetes.io/edge": "true"},
			want:   model.NodeCategoryCloudWorker,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Classify(nodeWithLabels("n", tt.labels)))
		})
	}
}

func TestProjectReferenceEdgeNode(t *testing.T) {
	r := Project(loadEdgeNode(t), Config{})

	assert.Equal(t, model.KindNode, r.Kind)
	assert.Equal(t, "nodes/edge-worker/u2deb-edge", r.Key)
	assert.Equal(t, "1101990", r.Source.ResourceVersion)
	require.NoError(t, r.Validate())

	spec, ok := r.Spec.(*model.NodeSpec)
	require.True(t, ok)
	assert.Equal(t, "u2deb-edge", spec.Name)
	assert.Equal(t, model.NodeCategoryEdgeWorker, spec.Category)
	assert.Len(t, spec.Labels, 7)
	assert.Equal(t, "amd64", spec.Labels["kubernetes.io/arch"])
	assert.Nil(t, spec.Annotations, "annotations are not published by default")

	assert.Equal(t, []model.NodeAddress{
		{Type: "InternalIP", Address: "10.0.2.15"},
		{Type: "Hostname", Address: "u2deb-edge"},
	}, spec.Addresses)

	assert.Equal(t, map[string]string{
		"cpu":               "2",
		"ephemeral-storage": "4027052Ki",
		"hugepages-2Mi":     "0",
		"memory":            "4093004Ki",
		"pods":              "110",
	}, spec.Capacity)
	assert.Equal(t, "2", spec.Allocatable["cpu"])
	assert.Equal(t, "3990604Ki", spec.Allocatable["memory"])
	assert.Equal(t, "3711331118", spec.Allocatable["ephemeral-storage"])

	assert.Equal(t, "True", spec.Ready)
	assert.Equal(t, "2026-11-22T20:16:51Z", spec.LastHeartbeatTime)
	require.Len(t, spec.Conditions, 4)
	assert.Equal(t, model.NodeCondition{
		Type:               "Ready",
		Status:             "True",
		Reason:             "EdgeReady",
		Message:            "edge is posting ready status",
		LastTransitionTime: "2026-11-05T16:39:28Z",
	}, spec.Conditions[3])

	assert.Equal(t, model.NodeInfo{
		MachineID:               "ec7fb05938a643dda6da0de11af71303",
		SystemUUID:              "30a2531c-ef27-8b4f-ade2-c4caa35e8b13",
		BootID:                  "0688abab-08dc-46f7-baf9-874b5ec79437",
		KernelVersion:           "6.12.115+deb13.1-amd64",
		OSImage:                 "Debian GNU/Linux 13 (trixie)",
		ContainerRuntimeVersion: "containerd://2.4.2",
		KubeletVersion:          "v1.32.10-continuumx-v1.23.0-6+2e0beec718a64b",
		KubeProxyVersion:        "v0.0.0-master+$Format:%H$",
		OperatingSystem:         "linux",
		Architecture:            "amd64",
	}, spec.NodeInfo)

	require.Len(t, spec.RuntimeHandlers, 2)
	assert.Equal(t, "", spec.RuntimeHandlers[0].Name)
	assert.Equal(t, "runc", spec.RuntimeHandlers[1].Name)
	require.NotNil(t, spec.RuntimeHandlers[1].Features)
	assert.True(t, *spec.RuntimeHandlers[1].Features.RecursiveReadOnlyMounts)
	assert.True(t, *spec.RuntimeHandlers[1].Features.UserNamespaces)
}

func TestProjectAnnotationAllowlist(t *testing.T) {
	node := loadEdgeNode(t)

	r := Project(node, Config{AnnotationKeys: []string{"node.alpha.kubernetes.io/ttl", "not-present"}})
	spec := r.Spec.(*model.NodeSpec)
	assert.Equal(t, map[string]string{"node.alpha.kubernetes.io/ttl": "0"}, spec.Annotations)

	r = Project(node, Config{AnnotationKeys: []string{"not-present"}})
	assert.Nil(t, r.Spec.(*model.NodeSpec).Annotations)
}

func TestProjectWithoutReadyCondition(t *testing.T) {
	r := Project(nodeWithLabels("worker-1", nil), Config{})
	spec := r.Spec.(*model.NodeSpec)
	assert.Equal(t, "nodes/cloud-worker/worker-1", r.Key)
	assert.Equal(t, "Unknown", spec.Ready)
	assert.Empty(t, spec.LastHeartbeatTime)
	assert.Nil(t, spec.Conditions)
	assert.Nil(t, spec.Capacity)
}

func TestProjectDoesNotAliasNodeMaps(t *testing.T) {
	node := loadEdgeNode(t)
	spec := Project(node, Config{}).Spec.(*model.NodeSpec)
	node.Labels["kubernetes.io/arch"] = "arm64"
	assert.Equal(t, "amd64", spec.Labels["kubernetes.io/arch"])
}

func TestHeartbeatOnlyChangesLastHeartbeatTime(t *testing.T) {
	node := loadEdgeNode(t)
	before := Project(node, Config{}).Spec.(*model.NodeSpec)

	for i := range node.Status.Conditions {
		node.Status.Conditions[i].LastHeartbeatTime = metav1.NewTime(node.Status.Conditions[i].LastHeartbeatTime.Add(60e9))
	}
	after := Project(node, Config{}).Spec.(*model.NodeSpec)

	assert.Equal(t, before.Conditions, after.Conditions)
	assert.Equal(t, "2026-11-22T20:17:51Z", after.LastHeartbeatTime)
}
