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

package access

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The consumer names are test identities, not partner components.
var (
	orchestrator = Identity{Authenticated: true, Name: "consumer-a"}
	scoped       = Identity{Authenticated: true, Name: "consumer-b"}
	stranger     = Identity{Authenticated: true, Name: "consumer-x"}
)

func TestAllowAll(t *testing.T) {
	var a Authorizer = AllowAll{}
	assert.NoError(t, a.Admit(Anonymous))
	assert.NoError(t, a.Authorize(Anonymous, ""))
	assert.NoError(t, a.Authorize(stranger, "nodes/"))
}

func TestNewStaticValidates(t *testing.T) {
	for name, consumers := range map[string][]Consumer{
		"none":            nil,
		"no name":         {{Prefixes: []string{"nodes/"}}},
		"twice":           {{Name: "a", Prefixes: []string{"nodes/"}}, {Name: "a", Prefixes: []string{"devices/"}}},
		"no prefixes":     {{Name: "a"}},
		"partial segment": {{Name: "a", Prefixes: []string{"devices/def"}}},
		"wildcard":        {{Name: "a", Prefixes: []string{"devices/*/"}}},
		"empty segment":   {{Name: "a", Prefixes: []string{"devices//"}}},
		"leading slash":   {{Name: "a", Prefixes: []string{"/devices/"}}},
	} {
		_, err := NewStatic(consumers)
		assert.Error(t, err, name)
	}
}

func TestStatic(t *testing.T) {
	s, err := NewStatic([]Consumer{
		{Name: "consumer-a", Prefixes: []string{"nodes/", "devices/"}},
		{Name: "consumer-b", Prefixes: []string{"devices/default/", "nodes/edge-worker/"}},
	})
	require.NoError(t, err)

	assert.ErrorIs(t, s.Admit(Anonymous), ErrUnauthenticated)
	assert.ErrorIs(t, s.Authorize(Anonymous, "nodes/"), ErrUnauthenticated)
	assert.ErrorIs(t, s.Admit(stranger), ErrForbidden)
	assert.ErrorIs(t, s.Authorize(stranger, "nodes/"), ErrForbidden)
	assert.ErrorIs(t, s.Admit(Identity{Authenticated: true}), ErrForbidden, "a verified certificate without a common name")

	assert.NoError(t, s.Admit(orchestrator))
	for _, prefix := range []string{"nodes/", "nodes/edge-worker/", "devices/", "devices/default/x"} {
		assert.NoError(t, s.Authorize(orchestrator, prefix), prefix)
	}
	assert.ErrorIs(t, s.Authorize(orchestrator, ""), ErrForbidden, "broader than every allowed prefix: refused, not narrowed")
	assert.ErrorIs(t, s.Authorize(orchestrator, "metrics/"), ErrForbidden)

	// A key, a narrower prefix and a partial key segment under a grant are all covered: a
	// stream on any of them only delivers keys under the grant.
	for _, prefix := range []string{"nodes/edge-worker/u2deb-edge", "devices/default/", "devices/default/automotive-tire", "devices/default/automotive-tire-pressure-ecu-fw-001"} {
		assert.NoError(t, s.Authorize(scoped, prefix), prefix)
	}
	assert.ErrorIs(t, s.Authorize(scoped, "nodes/"), ErrForbidden)
	assert.ErrorIs(t, s.Authorize(scoped, "nodes/cloud-worker/w1"), ErrForbidden)
	assert.ErrorIs(t, s.Authorize(scoped, "devices/vehicle-a/automotive-thermal-ecu-fw-001"), ErrForbidden)
	assert.ErrorIs(t, s.Authorize(scoped, "devices/default"), ErrForbidden, "without the separator it would also select devices/default-2/")
	assert.ErrorContains(t, s.Authorize(scoped, "devices/"), `"consumer-b" may not read "devices/"`)
}

func TestStaticCopiesPrefixes(t *testing.T) {
	consumers := []Consumer{{Name: "a", Prefixes: []string{"nodes/"}}}
	s, err := NewStatic(consumers)
	require.NoError(t, err)
	consumers[0].Prefixes[0] = ""
	assert.ErrorIs(t, s.Authorize(Identity{Authenticated: true, Name: "a"}, "devices/"), ErrForbidden)
}
