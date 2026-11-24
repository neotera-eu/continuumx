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

// Package node publishes Kubernetes nodes: cloud control plane, cloud worker and edge worker.
package node

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source"
)

// Name is the name of the node source.
const Name = "nodes"

// Source publishes the nodes seen by a node informer. The caller starts the informer.
type Source struct {
	informer cache.SharedIndexInformer
	cfg      Config

	mu   sync.Mutex
	keys map[string]string // node name -> published key
}

var _ source.Source = (*Source)(nil)

// New creates a node source on a node informer.
func New(informer cache.SharedIndexInformer, cfg Config) *Source {
	return &Source{
		informer: informer,
		cfg:      cfg,
		keys:     make(map[string]string),
	}
}

// Name returns the source name.
func (s *Source) Name() string {
	return Name
}

// Run feeds sink with node changes until ctx is done. Cancelling ctx is a normal stop and
// returns nil, also while the informer cache is still syncing.
func (s *Source) Run(ctx context.Context, sink source.Sink) error {
	registration, err := s.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { s.upsert(sink, obj) },
		UpdateFunc: func(_, obj interface{}) { s.upsert(sink, obj) },
		DeleteFunc: func(obj interface{}) { s.delete(sink, obj) },
	})
	if err != nil {
		return fmt.Errorf("register node event handler: %w", err)
	}
	defer func() {
		if err := s.informer.RemoveEventHandler(registration); err != nil {
			klog.Warningf("dtpublisher: remove node event handler: %v", err)
		}
	}()

	// WaitForNamedCacheSync returns false only when ctx is done.
	if cache.WaitForNamedCacheSync("dtpublisher-nodes", ctx.Done(), registration.HasSynced) {
		<-ctx.Done()
	}
	return nil
}

func (s *Source) upsert(sink source.Sink, obj interface{}) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		klog.Warningf("dtpublisher: unexpected object %T in node informer", obj)
		return
	}
	resource := Project(node, s.cfg)

	s.mu.Lock()
	defer s.mu.Unlock()

	// A category change moves the node to another key.
	if old, ok := s.keys[node.Name]; ok && old != resource.Key {
		sink.Delete(old)
	}
	s.keys[node.Name] = resource.Key
	if _, err := sink.Upsert(resource); err != nil {
		klog.Errorf("dtpublisher: publish node %s: %v", node.Name, err)
	}
}

func (s *Source) delete(sink source.Sink, obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	node, ok := obj.(*corev1.Node)
	if !ok {
		klog.Warningf("dtpublisher: unexpected object %T in node informer", obj)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if key, ok := s.keys[node.Name]; ok {
		sink.Delete(key)
		delete(s.keys, node.Name)
	}
}
