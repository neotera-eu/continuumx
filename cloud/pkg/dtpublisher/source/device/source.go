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

// Package device publishes device twins of devices reached through a mapper, joined from
// Device, DeviceModel and DeviceStatus (devices.kubeedge.io/v1beta1).
package device

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubeedge/api/apis/devices/v1beta1"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source"
)

// Name is the name of the device source.
const Name = "devices"

// Informers are the informers the device source reads. The caller starts them.
type Informers struct {
	Devices        cache.SharedIndexInformer
	DeviceModels   cache.SharedIndexInformer
	DeviceStatuses cache.SharedIndexInformer
}

// Source publishes one resource per Device. A change of the Device, of its DeviceStatus
// (same namespace and name) or of its DeviceModel republishes the device. Until all three
// caches are synced, events are ignored; the source then publishes every device once, so no
// device is published from a partly loaded cache.
type Source struct {
	informers Informers
	cfg       Config

	mu     sync.Mutex
	sink   source.Sink
	synced bool
}

var _ source.Source = (*Source)(nil)

// New creates a device source.
func New(informers Informers, cfg Config) *Source {
	return &Source{informers: informers, cfg: cfg}
}

// Name returns the source name.
func (s *Source) Name() string {
	return Name
}

// Run feeds sink with device changes until ctx is done. Cancelling ctx is a normal stop and
// returns nil, also while the caches are still syncing.
func (s *Source) Run(ctx context.Context, sink source.Sink) error {
	s.mu.Lock()
	s.sink = sink
	s.synced = false
	s.mu.Unlock()

	byObjectKey := func(obj interface{}) {
		if namespace, name, ok := objectKey(obj); ok {
			s.syncDevice(namespace, name)
		}
	}
	byModel := func(obj interface{}) {
		if namespace, name, ok := objectKey(obj); ok {
			s.syncModel(namespace, name)
		}
	}
	handlers := []struct {
		informer cache.SharedIndexInformer
		handle   func(obj interface{})
	}{
		{s.informers.Devices, byObjectKey},
		{s.informers.DeviceStatuses, byObjectKey}, // a DeviceStatus has its Device's name
		{s.informers.DeviceModels, byModel},
	}

	var synced []cache.InformerSynced
	for _, h := range handlers {
		handle := h.handle
		registration, err := h.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    handle,
			UpdateFunc: func(_, obj interface{}) { handle(obj) },
			DeleteFunc: handle,
		})
		if err != nil {
			return fmt.Errorf("register device event handler: %w", err)
		}
		informer := h.informer
		defer func() {
			if err := informer.RemoveEventHandler(registration); err != nil {
				klog.Warningf("dtpublisher: remove device event handler: %v", err)
			}
		}()
		synced = append(synced, registration.HasSynced)
	}

	// WaitForNamedCacheSync returns false only when ctx is done.
	if !cache.WaitForNamedCacheSync("dtpublisher-devices", ctx.Done(), synced...) {
		return nil
	}
	s.resyncAll()
	<-ctx.Done()
	return nil
}

func (s *Source) resyncAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.synced = true
	for _, obj := range s.informers.Devices.GetIndexer().List() {
		if d, ok := obj.(*v1beta1.Device); ok {
			s.syncLocked(d.Namespace, d.Name)
		}
	}
}

func (s *Source) syncDevice(namespace, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.synced {
		s.syncLocked(namespace, name)
	}
}

// syncModel republishes the devices of namespace that refer to the model.
func (s *Source) syncModel(namespace, modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.synced {
		return
	}
	for _, obj := range s.informers.Devices.GetIndexer().List() {
		d, ok := obj.(*v1beta1.Device)
		if !ok || d.Namespace != namespace || d.Spec.DeviceModelRef == nil || d.Spec.DeviceModelRef.Name != modelName {
			continue
		}
		s.syncLocked(d.Namespace, d.Name)
	}
}

// syncLocked publishes the current state of one device from the caches, or deletes it.
// It must be called with s.mu held.
func (s *Source) syncLocked(namespace, name string) {
	key := model.DeviceKey(namespace, name)
	device, ok := getObject[v1beta1.Device](s.informers.Devices, namespace, name)
	if !ok {
		s.sink.Delete(key)
		return
	}
	var devModel *v1beta1.DeviceModel
	if device.Spec.DeviceModelRef != nil {
		devModel, _ = getObject[v1beta1.DeviceModel](s.informers.DeviceModels, namespace, device.Spec.DeviceModelRef.Name)
	}
	status, _ := getObject[v1beta1.DeviceStatus](s.informers.DeviceStatuses, namespace, name)

	if _, err := s.sink.Upsert(Project(device, devModel, status, s.cfg)); err != nil {
		klog.Errorf("dtpublisher: publish device %s/%s: %v", namespace, name, err)
	}
}

// getObject returns the cached object namespace/name of type T, or false if there is none.
func getObject[T any](informer cache.SharedIndexInformer, namespace, name string) (*T, bool) {
	obj, exists, err := informer.GetIndexer().GetByKey(namespace + "/" + name)
	if err != nil || !exists {
		return nil, false
	}
	typed, ok := obj.(*T)
	return typed, ok
}

// objectKey returns the namespace and name of an informer object or tombstone.
func objectKey(obj interface{}) (string, string, bool) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		klog.Warningf("dtpublisher: object key of %T: %v", obj, err)
		return "", "", false
	}
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Warningf("dtpublisher: split object key %q: %v", key, err)
		return "", "", false
	}
	return namespace, name, true
}
