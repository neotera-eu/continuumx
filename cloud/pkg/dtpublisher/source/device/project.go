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

package device

import (
	"github.com/kubeedge/api/apis/devices/v1beta1"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source/internal/maputil"
)

// Config selects what the device source publishes.
type Config struct {
	// AnnotationKeys lists the Device annotation keys to publish. Empty publishes none.
	AnnotationKeys []string
}

// Project builds the published resource of a device twin. devModel and status may be nil
// when the DeviceModel or the DeviceStatus does not exist (yet).
func Project(device *v1beta1.Device, devModel *v1beta1.DeviceModel, status *v1beta1.DeviceStatus, cfg Config) model.Resource {
	spec := &model.DeviceSpec{
		Namespace:   device.Namespace,
		Name:        device.Name,
		Protocol:    device.Spec.Protocol.ProtocolName,
		Node:        device.Spec.NodeName,
		Labels:      maputil.Copy(device.Labels),
		Annotations: maputil.Select(device.Annotations, cfg.AnnotationKeys),
	}
	if device.Spec.DeviceModelRef != nil {
		spec.Model = device.Spec.DeviceModelRef.Name
	}
	if spec.Protocol == "" && devModel != nil {
		spec.Protocol = devModel.Spec.Protocol
	}
	if status != nil {
		spec.State = status.Status.State
		spec.LastOnlineTime = status.Status.LastOnlineTime
	}
	spec.Properties = projectProperties(device, devModel, status)

	resourceVersion := device.ResourceVersion
	if status != nil {
		// The twin values come from the DeviceStatus; reference both object versions.
		resourceVersion = device.ResourceVersion + "," + status.ResourceVersion
	}
	return model.Resource{
		Kind:   model.KindDevice,
		Key:    model.DeviceKey(device.Namespace, device.Name),
		Source: model.SourceRef{ResourceVersion: resourceVersion},
		Spec:   spec,
	}
}

// projectProperties lists the model properties in model order, then the properties the
// Device declares that the model does not. Twins of properties in neither are dropped, as the
// Device Controller drops them on upload.
func projectProperties(device *v1beta1.Device, devModel *v1beta1.DeviceModel, status *v1beta1.DeviceStatus) []model.DeviceProperty {
	declared := make(map[string]*v1beta1.DeviceProperty, len(device.Spec.Properties))
	for i := range device.Spec.Properties {
		declared[device.Spec.Properties[i].Name] = &device.Spec.Properties[i]
	}
	twins := make(map[string]*v1beta1.Twin)
	if status != nil {
		for i := range status.Status.Twins {
			twins[status.Status.Twins[i].PropertyName] = &status.Status.Twins[i]
		}
	}

	var out []model.DeviceProperty
	inModel := make(map[string]bool)
	if devModel != nil {
		for _, mp := range devModel.Spec.Properties {
			inModel[mp.Name] = true
			p := model.DeviceProperty{
				Name:        mp.Name,
				Description: mp.Description,
				Type:        string(mp.Type),
				AccessMode:  string(mp.AccessMode),
				Unit:        mp.Unit,
				Minimum:     mp.Minimum,
				Maximum:     mp.Maximum,
			}
			fillValues(&p, declared[mp.Name], twins[mp.Name])
			out = append(out, p)
		}
	}
	for i := range device.Spec.Properties {
		dp := &device.Spec.Properties[i]
		if inModel[dp.Name] {
			continue
		}
		p := model.DeviceProperty{Name: dp.Name}
		fillValues(&p, dp, twins[dp.Name])
		out = append(out, p)
	}
	return out
}

func fillValues(p *model.DeviceProperty, declared *v1beta1.DeviceProperty, twin *v1beta1.Twin) {
	if declared != nil {
		p.Desired = twinValue(declared.Desired)
	}
	if twin != nil {
		p.ObservedDesired = twinValue(twin.ObservedDesired)
		p.Reported = twinValue(twin.Reported)
	}
}

// twinValue returns nil for an unset value: no value and no metadata.
func twinValue(in v1beta1.TwinProperty) *model.TwinValue {
	if in.Value == "" && len(in.Metadata) == 0 {
		return nil
	}
	return &model.TwinValue{Value: in.Value, Metadata: maputil.Copy(in.Metadata)}
}
