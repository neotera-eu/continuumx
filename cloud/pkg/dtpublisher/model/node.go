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

package model

// NodeCategory is the role of a node in the continuum.
type NodeCategory string

const (
	// NodeCategoryCloudControlPlane is a cloud node that runs the Kubernetes control plane.
	NodeCategoryCloudControlPlane NodeCategory = "cloud-control-plane"
	// NodeCategoryCloudWorker is a cloud node that is neither control plane nor edge.
	NodeCategoryCloudWorker NodeCategory = "cloud-worker"
	// NodeCategoryEdgeWorker is an edge node running edgecore, for example a Central ECU.
	NodeCategoryEdgeWorker NodeCategory = "edge-worker"
)

// NodeSpec is the published view of a Kubernetes node, projected from v1.Node.
// Times are RFC 3339 in UTC; an empty string means the time is not set.
type NodeSpec struct {
	// Name is the node name.
	Name string `json:"name"`
	// Category is the role of the node, see NodeCategory.
	Category NodeCategory `json:"category"`
	// Labels are all node labels.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are the node annotations whose keys are allowlisted. None by default.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Addresses are the node addresses.
	Addresses []NodeAddress `json:"addresses,omitempty"`
	// Capacity is the total amount of each resource, as Kubernetes quantities.
	Capacity map[string]string `json:"capacity,omitempty"`
	// Allocatable is the amount of each resource available to pods, as Kubernetes quantities.
	Allocatable map[string]string `json:"allocatable,omitempty"`
	// Conditions are the node conditions, without per-condition heartbeat times.
	Conditions []NodeCondition `json:"conditions,omitempty"`
	// Ready is the status of the Ready condition: "True", "False" or "Unknown".
	Ready string `json:"ready"`
	// LastHeartbeatTime is the last heartbeat time of the Ready condition.
	LastHeartbeatTime string `json:"lastHeartbeatTime,omitempty"`
	// NodeInfo is the node system information.
	NodeInfo NodeInfo `json:"nodeInfo"`
	// RuntimeHandlers are the container runtime handlers and their features.
	RuntimeHandlers []RuntimeHandler `json:"runtimeHandlers,omitempty"`
}

// NodeAddress is one address of a node.
type NodeAddress struct {
	// Type is the address type, for example "InternalIP" or "Hostname".
	Type string `json:"type"`
	// Address is the address value.
	Address string `json:"address"`
}

// NodeCondition is one node condition.
type NodeCondition struct {
	// Type is the condition type, for example "Ready" or "MemoryPressure".
	Type string `json:"type"`
	// Status is "True", "False" or "Unknown".
	Status string `json:"status"`
	// Reason is a machine-readable reason for the last transition.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable message for the last transition.
	Message string `json:"message,omitempty"`
	// LastTransitionTime is when the status last changed.
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// NodeInfo is the system information of a node.
type NodeInfo struct {
	MachineID               string `json:"machineID,omitempty"`
	SystemUUID              string `json:"systemUUID,omitempty"`
	BootID                  string `json:"bootID,omitempty"`
	KernelVersion           string `json:"kernelVersion,omitempty"`
	OSImage                 string `json:"osImage,omitempty"`
	ContainerRuntimeVersion string `json:"containerRuntimeVersion,omitempty"`
	KubeletVersion          string `json:"kubeletVersion,omitempty"`
	KubeProxyVersion        string `json:"kubeProxyVersion,omitempty"`
	OperatingSystem         string `json:"operatingSystem,omitempty"`
	Architecture            string `json:"architecture,omitempty"`
}

// RuntimeHandler is a container runtime handler of a node.
type RuntimeHandler struct {
	// Name is the handler name; empty for the default handler.
	Name string `json:"name"`
	// Features are the features the handler supports, if reported.
	Features *RuntimeHandlerFeatures `json:"features,omitempty"`
}

// RuntimeHandlerFeatures are the features of a container runtime handler.
type RuntimeHandlerFeatures struct {
	// RecursiveReadOnlyMounts reports support for recursive read-only mounts, if known.
	RecursiveReadOnlyMounts *bool `json:"recursiveReadOnlyMounts,omitempty"`
	// UserNamespaces reports support for user namespaces, if known.
	UserNamespaces *bool `json:"userNamespaces,omitempty"`
}
