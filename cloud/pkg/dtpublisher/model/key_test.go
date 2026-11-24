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

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNodeKey(t *testing.T) {
	assert.Equal(t, "nodes/edge-worker/u2deb-edge", NodeKey(NodeCategoryEdgeWorker, "u2deb-edge"))
	assert.Equal(t, "nodes/cloud-control-plane/u2deb-cloud", NodeKey(NodeCategoryCloudControlPlane, "u2deb-cloud"))
	assert.Equal(t, "nodes/cloud-worker/worker-1", NodeKey(NodeCategoryCloudWorker, "worker-1"))
}

func TestValidateKey(t *testing.T) {
	tests := []struct {
		key     string
		wantErr bool
	}{
		{key: "nodes/edge-worker/u2deb-edge"},
		{key: "devices/default/automotive-tire-pressure-ecu-fw-001"},
		{key: "", wantErr: true},
		{key: "nodes", wantErr: true},
		{key: "nodes//u2deb-edge", wantErr: true},
		{key: "nodes/edge-worker/", wantErr: true},
		{key: "nodes/*/u2deb-edge", wantErr: true},
		{key: "nodes/edge-worker/u2deb-edge?", wantErr: true},
	}
	for _, tt := range tests {
		err := ValidateKey(tt.key)
		if tt.wantErr {
			assert.Error(t, err, "key %q", tt.key)
		} else {
			assert.NoError(t, err, "key %q", tt.key)
		}
	}
}

func TestMatchesPrefix(t *testing.T) {
	key := NodeKey(NodeCategoryEdgeWorker, "u2deb-edge")
	assert.True(t, MatchesPrefix(key, ""))
	assert.True(t, MatchesPrefix(key, "nodes/"))
	assert.True(t, MatchesPrefix(key, "nodes/edge-worker/"))
	assert.False(t, MatchesPrefix(key, "nodes/cloud-worker/"))
	assert.False(t, MatchesPrefix(key, "devices/"))
}

func TestResourceValidate(t *testing.T) {
	valid := Resource{Kind: KindNode, Key: "nodes/edge-worker/u2deb-edge", Spec: &NodeSpec{}}
	assert.NoError(t, valid.Validate())

	noKind := valid
	noKind.Kind = ""
	assert.Error(t, noKind.Validate())

	badKey := valid
	badKey.Key = "nodes"
	assert.Error(t, badKey.Validate())

	noSpec := valid
	noSpec.Spec = nil
	assert.Error(t, noSpec.Validate())
}
