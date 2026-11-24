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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/neotera-eu/continuumx/cloud/pkg/common/nodes"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

// ControlPlaneRoleLabel marks a cloud control-plane node (kubeadm convention).
const ControlPlaneRoleLabel = "node-role.kubernetes.io/control-plane"

// Config selects what the node source publishes.
type Config struct {
	// AnnotationKeys lists the node annotation keys to publish. Empty publishes none.
	AnnotationKeys []string
}

// Classify returns the category of a node. An edge node, as ContinuumX defines it
// (nodes.IsEdgeNode), is an edge worker even if it also carries the control-plane label.
func Classify(node *corev1.Node) model.NodeCategory {
	if nodes.IsEdgeNode(node) {
		return model.NodeCategoryEdgeWorker
	}
	if _, ok := node.Labels[ControlPlaneRoleLabel]; ok {
		return model.NodeCategoryCloudControlPlane
	}
	return model.NodeCategoryCloudWorker
}

// Project builds the published resource of a node.
func Project(node *corev1.Node, cfg Config) model.Resource {
	category := Classify(node)
	spec := &model.NodeSpec{
		Name:            node.Name,
		Category:        category,
		Labels:          copyMap(node.Labels),
		Annotations:     selectKeys(node.Annotations, cfg.AnnotationKeys),
		Addresses:       projectAddresses(node.Status.Addresses),
		Capacity:        projectResources(node.Status.Capacity),
		Allocatable:     projectResources(node.Status.Allocatable),
		Conditions:      projectConditions(node.Status.Conditions),
		Ready:           string(corev1.ConditionUnknown),
		NodeInfo:        projectNodeInfo(node.Status.NodeInfo),
		RuntimeHandlers: projectRuntimeHandlers(node.Status.RuntimeHandlers),
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			spec.Ready = string(c.Status)
			spec.LastHeartbeatTime = formatTime(c.LastHeartbeatTime)
			break
		}
	}
	return model.Resource{
		Kind:   model.KindNode,
		Key:    model.NodeKey(category, node.Name),
		Source: model.SourceRef{ResourceVersion: node.ResourceVersion},
		Spec:   spec,
	}
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func selectKeys(in map[string]string, keys []string) map[string]string {
	var out map[string]string
	for _, k := range keys {
		v, ok := in[k]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(keys))
		}
		out[k] = v
	}
	return out
}

func projectAddresses(in []corev1.NodeAddress) []model.NodeAddress {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.NodeAddress, 0, len(in))
	for _, a := range in {
		out = append(out, model.NodeAddress{Type: string(a.Type), Address: a.Address})
	}
	return out
}

func projectResources(in corev1.ResourceList) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for name, quantity := range in {
		out[string(name)] = quantity.String()
	}
	return out
}

// projectConditions leaves out per-condition heartbeat times, so a heartbeat alone changes only
// NodeSpec.LastHeartbeatTime.
func projectConditions(in []corev1.NodeCondition) []model.NodeCondition {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.NodeCondition, 0, len(in))
	for _, c := range in {
		out = append(out, model.NodeCondition{
			Type:               string(c.Type),
			Status:             string(c.Status),
			Reason:             c.Reason,
			Message:            c.Message,
			LastTransitionTime: formatTime(c.LastTransitionTime),
		})
	}
	return out
}

func projectNodeInfo(in corev1.NodeSystemInfo) model.NodeInfo {
	return model.NodeInfo{
		MachineID:               in.MachineID,
		SystemUUID:              in.SystemUUID,
		BootID:                  in.BootID,
		KernelVersion:           in.KernelVersion,
		OSImage:                 in.OSImage,
		ContainerRuntimeVersion: in.ContainerRuntimeVersion,
		KubeletVersion:          in.KubeletVersion,
		KubeProxyVersion:        in.KubeProxyVersion,
		OperatingSystem:         in.OperatingSystem,
		Architecture:            in.Architecture,
	}
}

func projectRuntimeHandlers(in []corev1.NodeRuntimeHandler) []model.RuntimeHandler {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.RuntimeHandler, 0, len(in))
	for _, h := range in {
		handler := model.RuntimeHandler{Name: h.Name}
		if h.Features != nil {
			handler.Features = &model.RuntimeHandlerFeatures{
				RecursiveReadOnlyMounts: copyBool(h.Features.RecursiveReadOnlyMounts),
				UserNamespaces:          copyBool(h.Features.UserNamespaces),
			}
		}
		out = append(out, handler)
	}
	return out
}

func copyBool(in *bool) *bool {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func formatTime(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
