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

// DeviceSpec is the published view of a device twin, joined from the Device, its DeviceModel
// and its DeviceStatus (devices.kubeedge.io/v1beta1).
//
// The schema is structural: it carries what those objects say, typed by the model, and adds
// no domain semantics. Values are strings, as stored in DeviceStatus; Type tells how to read
// them.
type DeviceSpec struct {
	// Namespace is the namespace of the Device.
	Namespace string `json:"namespace"`
	// Name is the name of the Device.
	Name string `json:"name"`
	// Model is the name of the DeviceModel the Device refers to.
	Model string `json:"model,omitempty"`
	// Protocol is the protocol name of the Device, or of its model if the Device sets none.
	Protocol string `json:"protocol,omitempty"`
	// Node is the node the device is attached to, for example the Central ECU "u2deb-edge".
	// It matches NodeSpec.Name of an edge-worker node.
	Node string `json:"node,omitempty"`
	// Labels are all Device labels.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are the Device annotations whose keys are allowlisted. None by default.
	Annotations map[string]string `json:"annotations,omitempty"`
	// State is the device state reported by the mapper. Empty if never reported.
	State string `json:"state,omitempty"`
	// LastOnlineTime is when the device was last online, as reported by the mapper.
	LastOnlineTime string `json:"lastOnlineTime,omitempty"`
	// Properties are the device properties: those of the model, in model order, then any
	// property the Device declares that the model does not.
	Properties []DeviceProperty `json:"properties,omitempty"`
}

// DeviceProperty is one property of a device twin.
type DeviceProperty struct {
	// Name is the property name.
	Name string `json:"name"`
	// Description is the model description of the property.
	Description string `json:"description,omitempty"`
	// Type is the model value type: INT, FLOAT, DOUBLE, STRING, BOOLEAN, BYTES or STREAM.
	// Empty if the model is not known.
	Type string `json:"type,omitempty"`
	// AccessMode is ReadOnly or ReadWrite. Empty if the model is not known.
	AccessMode string `json:"accessMode,omitempty"`
	// Unit is the model unit, for example "kPa".
	Unit string `json:"unit,omitempty"`
	// Minimum is the model minimum, as a string.
	Minimum string `json:"minimum,omitempty"`
	// Maximum is the model maximum, as a string.
	Maximum string `json:"maximum,omitempty"`
	// Desired is the value requested on the cloud side (Device spec), if any.
	Desired *TwinValue `json:"desired,omitempty"`
	// ObservedDesired is the desired value as observed at the edge (DeviceStatus), if any.
	ObservedDesired *TwinValue `json:"observedDesired,omitempty"`
	// Reported is the value reported by the device (DeviceStatus), if any.
	Reported *TwinValue `json:"reported,omitempty"`
}

// TwinValue is one value of a device property with its metadata, as stored by ContinuumX.
// For reported values the Device Controller sets the metadata keys "timestamp" and "type".
type TwinValue struct {
	// Value is the value as a string.
	Value string `json:"value"`
	// Metadata is the value metadata.
	Metadata map[string]string `json:"metadata,omitempty"`
}
