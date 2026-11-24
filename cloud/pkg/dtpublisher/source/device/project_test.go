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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/kubeedge/api/apis/devices/v1beta1"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
)

const (
	tireDevice = "automotive-tire-pressure-ecu-fw-001"
	tireKey    = "devices/default/" + tireDevice
)

func load[T any](t *testing.T, file string) *T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
	require.NoError(t, err)
	obj := new(T)
	require.NoError(t, yaml.Unmarshal(data, obj))
	return obj
}

func loadTire(t *testing.T) (*v1beta1.Device, *v1beta1.DeviceModel, *v1beta1.DeviceStatus) {
	return load[v1beta1.Device](t, "tire-pressure-device.yaml"),
		load[v1beta1.DeviceModel](t, "tire-pressure-model.yaml"),
		load[v1beta1.DeviceStatus](t, "tire-pressure-status.yaml")
}

func property(t *testing.T, spec *model.DeviceSpec, name string) model.DeviceProperty {
	t.Helper()
	for _, p := range spec.Properties {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("property %q not published", name)
	return model.DeviceProperty{}
}

func names(spec *model.DeviceSpec) []string {
	out := make([]string, 0, len(spec.Properties))
	for _, p := range spec.Properties {
		out = append(out, p.Name)
	}
	return out
}

func TestProjectTirePressureDevice(t *testing.T) {
	device, devModel, status := loadTire(t)
	r := Project(device, devModel, status, Config{})

	assert.Equal(t, model.KindDevice, r.Kind)
	assert.Equal(t, tireKey, r.Key)
	assert.Equal(t, "200,300", r.Source.ResourceVersion)
	require.NoError(t, r.Validate())

	spec := r.Spec.(*model.DeviceSpec)
	assert.Equal(t, "default", spec.Namespace)
	assert.Equal(t, tireDevice, spec.Name)
	assert.Equal(t, "automotive-tire-pressure-ecu-fw-demo", spec.Model)
	assert.Equal(t, "virtual-ethernet", spec.Protocol)
	assert.Equal(t, "u2deb-edge", spec.Node)
	assert.Equal(t, "tire-pressure", spec.Labels["vehicle-domain"])
	assert.Nil(t, spec.Annotations, "annotations are not published by default")
	assert.Equal(t, "online", spec.State)
	assert.Equal(t, "2026-11-24T10:00:00Z", spec.LastOnlineTime)

	assert.Equal(t, []string{
		"frontLeftPressure", "lowPressureWarning", "lowPressureWheels",
		"firmwareVersion", "pressureThreshold", "leakEnabled",
	}, names(spec), "model order")

	assert.Equal(t, model.DeviceProperty{
		Name:        "frontLeftPressure",
		Description: "Front-left tire gauge pressure",
		Type:        "FLOAT",
		AccessMode:  "ReadOnly",
		Unit:        "kPa",
		Minimum:     "0",
		Maximum:     "500",
		Reported:    &model.TwinValue{Value: "230.0", Metadata: map[string]string{"timestamp": "1763978400000"}},
	}, property(t, spec, "frontLeftPressure"))

	threshold := property(t, spec, "pressureThreshold")
	assert.Equal(t, "ReadWrite", threshold.AccessMode)
	assert.Equal(t, &model.TwinValue{
		Value:    "210.0",
		Metadata: map[string]string{"source": "cloud", "reason": "initial-configuration"},
	}, threshold.Desired)
	assert.Equal(t, "210.0", threshold.ObservedDesired.Value)
	assert.Equal(t, "210.0", threshold.Reported.Value)

	warning := property(t, spec, "lowPressureWarning")
	assert.Equal(t, "BOOLEAN", warning.Type)
	assert.Nil(t, warning.Desired, "a read-only property has no desired value")
	assert.Nil(t, warning.ObservedDesired)
}

func TestProjectAnnotationAllowlist(t *testing.T) {
	device, devModel, status := loadTire(t)
	spec := Project(device, devModel, status, Config{AnnotationKeys: []string{"vehicle-subsystem", "absent"}}).Spec.(*model.DeviceSpec)
	assert.Equal(t, map[string]string{"vehicle-subsystem": "tire-pressure-monitoring"}, spec.Annotations)
}

func TestProjectWithoutStatus(t *testing.T) {
	device, devModel, _ := loadTire(t)
	r := Project(device, devModel, nil, Config{})
	assert.Equal(t, "200", r.Source.ResourceVersion)

	spec := r.Spec.(*model.DeviceSpec)
	assert.Empty(t, spec.State)
	assert.Len(t, spec.Properties, 6)
	for _, p := range spec.Properties {
		assert.Nil(t, p.Reported, p.Name)
		assert.Nil(t, p.ObservedDesired, p.Name)
	}
	assert.Equal(t, "sim-1.0.0", property(t, spec, "firmwareVersion").Desired.Value)
}

func TestProjectWithoutModel(t *testing.T) {
	device, _, status := loadTire(t)
	spec := Project(device, nil, status, Config{}).Spec.(*model.DeviceSpec)

	assert.Equal(t, "virtual-ethernet", spec.Protocol)
	assert.Equal(t, "automotive-tire-pressure-ecu-fw-demo", spec.Model)
	require.Len(t, spec.Properties, 6, "Device properties, in Device order")
	p := property(t, spec, "frontLeftPressure")
	assert.Empty(t, p.Type, "no type without a model")
	assert.Equal(t, "230.0", p.Reported.Value)
}

func TestProjectProtocolFromModel(t *testing.T) {
	device, devModel, status := loadTire(t)
	device.Spec.Protocol.ProtocolName = ""
	spec := Project(device, devModel, status, Config{}).Spec.(*model.DeviceSpec)
	assert.Equal(t, "virtual-ethernet", spec.Protocol)
}

func TestProjectPropertySets(t *testing.T) {
	device, devModel, status := loadTire(t)

	// The model gains a property the Device does not declare: published, without values.
	devModel.Spec.Properties = append(devModel.Spec.Properties, v1beta1.ModelProperty{
		Name: "rearLeftPressure", Type: v1beta1.FLOAT, AccessMode: v1beta1.ReadOnly, Unit: "kPa",
	})
	// The Device declares a property the model does not: published after the model ones.
	device.Spec.Properties = append(device.Spec.Properties, v1beta1.DeviceProperty{
		Name: "vendorDiagnostic", Desired: v1beta1.TwinProperty{Value: "on"},
	})
	// A twin of a property that is neither in the model nor in the Device: dropped.
	status.Status.Twins = append(status.Status.Twins, v1beta1.Twin{
		PropertyName: "removedProperty", Reported: v1beta1.TwinProperty{Value: "1"},
	})

	spec := Project(device, devModel, status, Config{}).Spec.(*model.DeviceSpec)
	assert.Equal(t, []string{
		"frontLeftPressure", "lowPressureWarning", "lowPressureWheels",
		"firmwareVersion", "pressureThreshold", "leakEnabled",
		"rearLeftPressure", "vendorDiagnostic",
	}, names(spec))

	rearLeft := property(t, spec, "rearLeftPressure")
	assert.Equal(t, "FLOAT", rearLeft.Type)
	assert.Nil(t, rearLeft.Desired)
	assert.Nil(t, rearLeft.Reported)

	vendor := property(t, spec, "vendorDiagnostic")
	assert.Empty(t, vendor.Type)
	assert.Equal(t, "on", vendor.Desired.Value)
}

func TestProjectDoesNotAliasObjects(t *testing.T) {
	device, devModel, status := loadTire(t)
	spec := Project(device, devModel, status, Config{}).Spec.(*model.DeviceSpec)
	device.Labels["vehicle-domain"] = "changed"
	device.Spec.Properties[4].Desired.Metadata["reason"] = "changed"
	status.Status.Twins[0].Reported.Metadata["timestamp"] = "0"

	assert.Equal(t, "tire-pressure", spec.Labels["vehicle-domain"])
	assert.Equal(t, "initial-configuration", property(t, spec, "pressureThreshold").Desired.Metadata["reason"])
	assert.Equal(t, "1763978400000", property(t, spec, "frontLeftPressure").Reported.Metadata["timestamp"])
}

// setTwin sets the reported (and, if observed is true, the observed-desired) value of a twin.
func setTwin(status *v1beta1.DeviceStatus, name, value string, observed bool) {
	for i := range status.Status.Twins {
		if status.Status.Twins[i].PropertyName == name {
			status.Status.Twins[i].Reported.Value = value
			if observed {
				status.Status.Twins[i].ObservedDesired.Value = value
			}
			return
		}
	}
}

// TestLeakScenarioThroughStore follows the playground's low-pressure exercise as a sequence of
// object updates and checks what a subscriber receives.
func TestLeakScenarioThroughStore(t *testing.T) {
	device, devModel, status := loadTire(t)
	st := store.New()
	publish := func() bool {
		changed, err := st.Upsert(Project(device, devModel, status, Config{}))
		require.NoError(t, err)
		return changed
	}
	require.True(t, publish())
	sub := st.Subscribe("devices/", 0)
	defer sub.Close()

	next := func() *model.DeviceSpec {
		t.Helper()
		ev := <-sub.Events()
		require.Equal(t, store.EventUpsert, ev.Type)
		require.Equal(t, tireKey, ev.Key)
		return ev.Resource.Spec.(*model.DeviceSpec)
	}

	// The same objects again: nothing is published.
	assert.False(t, publish())
	assert.Empty(t, sub.Events())

	// Cloud side: raise the threshold, then enable the leak.
	device.Spec.Properties[4].Desired.Value = "234"
	device.Spec.Properties[5].Desired.Value = "true"
	require.True(t, publish())
	spec := next()
	assert.Equal(t, "234", property(t, spec, "pressureThreshold").Desired.Value)
	assert.Equal(t, "true", property(t, spec, "leakEnabled").Desired.Value)
	assert.Equal(t, "false", property(t, spec, "leakEnabled").Reported.Value, "not yet applied at the edge")

	// Edge side: the ECU applies both and the pressure starts to fall.
	setTwin(status, "pressureThreshold", "234.0", true)
	setTwin(status, "leakEnabled", "true", true)
	setTwin(status, "frontLeftPressure", "236.0", false)
	require.True(t, publish())
	spec = next()
	assert.Equal(t, "true", property(t, spec, "leakEnabled").Reported.Value)
	assert.Equal(t, "236.0", property(t, spec, "frontLeftPressure").Reported.Value)
	assert.Equal(t, "false", property(t, spec, "lowPressureWarning").Reported.Value)

	// The pressure crosses the threshold: the warning is raised for the front-left wheel.
	setTwin(status, "frontLeftPressure", "232.5", false)
	setTwin(status, "lowPressureWarning", "true", false)
	setTwin(status, "lowPressureWheels", "FL", false)
	require.True(t, publish())
	spec = next()
	assert.Equal(t, "232.5", property(t, spec, "frontLeftPressure").Reported.Value)
	assert.Equal(t, "true", property(t, spec, "lowPressureWarning").Reported.Value)
	assert.Equal(t, "FL", property(t, spec, "lowPressureWheels").Reported.Value)
	assert.Empty(t, sub.Events())
}
