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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubeedge/api/apis/devices/v1beta1"
	"github.com/kubeedge/api/client/clientset/versioned/fake"
	crdinformers "github.com/kubeedge/api/client/informers/externalversions"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
)

const (
	waitTimeout  = 5 * time.Second
	pollInterval = 10 * time.Millisecond
	namespace    = "default"
)

type fixture struct {
	client *fake.Clientset
	store  *store.Store
}

// start creates the given objects, then runs a device source on a fake clientset.
func start(t *testing.T, objects ...interface{}) *fixture {
	t.Helper()
	f := &fixture{client: fake.NewSimpleClientset(), store: store.New()}
	for _, obj := range objects {
		f.create(t, obj)
	}

	ctx, cancel := context.WithCancel(context.Background())
	factory := crdinformers.NewSharedInformerFactory(f.client, 0)
	devices := factory.Devices().V1beta1()
	src := New(Informers{
		Devices:        devices.Devices().Informer(),
		DeviceModels:   devices.DeviceModels().Informer(),
		DeviceStatuses: devices.DeviceStatuses().Informer(),
	}, Config{})

	errCh := make(chan error, 1)
	go func() { errCh <- src.Run(ctx, f.store) }()
	factory.Start(ctx.Done())

	t.Cleanup(func() {
		cancel()
		factory.Shutdown()
		select {
		case err := <-errCh:
			assert.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Error("device source did not stop")
		}
	})
	return f
}

func (f *fixture) create(t *testing.T, obj interface{}) {
	t.Helper()
	ctx := context.Background()
	api := f.client.DevicesV1beta1()
	var err error
	switch o := obj.(type) {
	case *v1beta1.Device:
		_, err = api.Devices(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
	case *v1beta1.DeviceModel:
		_, err = api.DeviceModels(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
	case *v1beta1.DeviceStatus:
		_, err = api.DeviceStatuses(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
	default:
		t.Fatalf("unexpected object %T", obj)
	}
	require.NoError(t, err)
}

// eventually waits until the published tire device satisfies check.
func (f *fixture) eventually(t *testing.T, msg string, check func(spec *model.DeviceSpec) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		r, ok := f.store.Get(tireKey)
		return ok && check(r.Spec.(*model.DeviceSpec))
	}, waitTimeout, pollInterval, msg)
}

func (f *fixture) absent(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, ok := f.store.Get(tireKey)
		return !ok
	}, waitTimeout, pollInterval, "tire device still published")
}

func find(spec *model.DeviceSpec, name string) *model.DeviceProperty {
	for i := range spec.Properties {
		if spec.Properties[i].Name == name {
			return &spec.Properties[i]
		}
	}
	return nil
}

func reported(spec *model.DeviceSpec, name string) string {
	if p := find(spec, name); p != nil && p.Reported != nil {
		return p.Reported.Value
	}
	return ""
}

func TestSourcePublishesJoinedDeviceAtStart(t *testing.T) {
	device, devModel, status := loadTire(t)
	f := start(t, devModel, device, status)

	f.eventually(t, "joined tire device", func(spec *model.DeviceSpec) bool {
		p := find(spec, "frontLeftPressure")
		return p != nil && p.Type == "FLOAT" && reported(spec, "frontLeftPressure") == "230.0" && spec.Node == "u2deb-edge"
	})
	all, _ := f.store.List("")
	assert.Len(t, all, 1)
}

func TestSourceFollowsStatusChanges(t *testing.T) {
	ctx := context.Background()
	device, devModel, status := loadTire(t)
	f := start(t, devModel, device, status)
	f.eventually(t, "initial", func(spec *model.DeviceSpec) bool { return reported(spec, "frontLeftPressure") == "230.0" })

	current, err := f.client.DevicesV1beta1().DeviceStatuses(namespace).Get(ctx, tireDevice, metav1.GetOptions{})
	require.NoError(t, err)
	setTwin(current, "frontLeftPressure", "232.5", false)
	setTwin(current, "lowPressureWarning", "true", false)
	_, err = f.client.DevicesV1beta1().DeviceStatuses(namespace).Update(ctx, current, metav1.UpdateOptions{})
	require.NoError(t, err)
	f.eventually(t, "reported low pressure", func(spec *model.DeviceSpec) bool {
		return reported(spec, "frontLeftPressure") == "232.5" && reported(spec, "lowPressureWarning") == "true"
	})

	// Without a DeviceStatus the device is still published, without reported values.
	require.NoError(t, f.client.DevicesV1beta1().DeviceStatuses(namespace).Delete(ctx, tireDevice, metav1.DeleteOptions{}))
	f.eventually(t, "no reported values", func(spec *model.DeviceSpec) bool {
		return reported(spec, "frontLeftPressure") == "" && spec.State == ""
	})
}

func TestSourceFollowsModelChanges(t *testing.T) {
	ctx := context.Background()
	device, devModel, status := loadTire(t)

	// The Device and DeviceStatus exist before the model: published without types.
	f := start(t, device, status)
	f.eventually(t, "untyped device", func(spec *model.DeviceSpec) bool {
		p := find(spec, "frontLeftPressure")
		return p != nil && p.Type == "" && p.Reported != nil
	})

	// A model of the same name in another namespace changes nothing.
	other := devModel.DeepCopy()
	other.Namespace = "other"
	f.create(t, other)

	f.create(t, devModel)
	f.eventually(t, "typed device", func(spec *model.DeviceSpec) bool {
		p := find(spec, "frontLeftPressure")
		return p != nil && p.Type == "FLOAT" && p.Unit == "kPa"
	})

	current, err := f.client.DevicesV1beta1().DeviceModels(namespace).Get(ctx, devModel.Name, metav1.GetOptions{})
	require.NoError(t, err)
	current.Spec.Properties[0].Unit = "bar"
	_, err = f.client.DevicesV1beta1().DeviceModels(namespace).Update(ctx, current, metav1.UpdateOptions{})
	require.NoError(t, err)
	f.eventually(t, "unit from updated model", func(spec *model.DeviceSpec) bool {
		return find(spec, "frontLeftPressure").Unit == "bar"
	})
}

func TestSourceFollowsDeviceDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	device, devModel, status := loadTire(t)
	f := start(t, devModel, device, status)
	f.eventually(t, "initial", func(spec *model.DeviceSpec) bool { return spec.Name == tireDevice })
	first, _ := f.store.Get(tireKey)

	// A cloud-side change of a desired value is published.
	current, err := f.client.DevicesV1beta1().Devices(namespace).Get(ctx, tireDevice, metav1.GetOptions{})
	require.NoError(t, err)
	current.Spec.Properties[5].Desired.Value = "true"
	_, err = f.client.DevicesV1beta1().Devices(namespace).Update(ctx, current, metav1.UpdateOptions{})
	require.NoError(t, err)
	f.eventually(t, "desired leak", func(spec *model.DeviceSpec) bool {
		return find(spec, "leakEnabled").Desired.Value == "true"
	})

	require.NoError(t, f.client.DevicesV1beta1().Devices(namespace).Delete(ctx, tireDevice, metav1.DeleteOptions{}))
	f.absent(t)

	recreated, _, _ := loadTire(t)
	f.create(t, recreated)
	f.eventually(t, "recreated", func(spec *model.DeviceSpec) bool {
		return find(spec, "leakEnabled").Desired.Value == "false"
	})
	again, _ := f.store.Get(tireKey)
	assert.Greater(t, again.Revision, first.Revision)
}
