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

package httpsse

import (
	"net/http"
	"strings"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
)

// query wraps a list or get handler: it answers 404 with ReasonQueryDisabled unless the
// operations are enabled.
func (s *Server) query(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.opts.QueryEnabled {
			writeError(w, http.StatusNotFound, ReasonQueryDisabled, "list and get are not enabled on this publisher; subscribe with %s", WatchPath)
			return
		}
		h(w, r)
	}
}

func (s *Server) serveListNodes(w http.ResponseWriter, r *http.Request) {
	prefix := model.NodesPrefix + model.KeySeparator
	if category := r.URL.Query().Get(ParamCategory); category != "" {
		if !isNodeCategory(category) {
			writeError(w, http.StatusBadRequest, ReasonBadRequest, "invalid %s %q, valid: %v", ParamCategory, category, model.NodeCategories())
			return
		}
		prefix += category + model.KeySeparator
	}
	if !s.authorize(w, r, prefix) {
		return
	}
	s.writeList(w, prefix, nil)
}

func (s *Server) serveListDevices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := model.DevicesPrefix + model.KeySeparator
	if namespace := q.Get(ParamNamespace); namespace != "" {
		prefix += namespace + model.KeySeparator
	}
	if !s.authorize(w, r, prefix) {
		return
	}
	var keep func(model.Resource) bool
	if node := q.Get(ParamNode); node != "" {
		keep = func(r model.Resource) bool {
			spec, ok := r.Spec.(*model.DeviceSpec)
			return ok && spec.Node == node
		}
	}
	s.writeList(w, prefix, keep)
}

func (s *Server) writeList(w http.ResponseWriter, prefix string, keep func(model.Resource) bool) {
	items, revision := s.store.List(prefix)
	if keep != nil {
		kept := items[:0]
		for _, r := range items {
			if keep(r) {
				kept = append(kept, r)
			}
		}
		items = kept
	}
	writeJSON(w, http.StatusOK, List{Cursor: Cursor{Epoch: s.store.Epoch(), Revision: revision}, Items: items})
}

// serveGetNode finds a node by name. Node names are unique in a cluster, so the category
// is not part of the request. Only the categories the consumer may read are searched, so the
// answer does not tell whether a node exists in another category.
func (s *Server) serveGetNode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := security.Identity(r.TLS)
	var refused error
	searched := 0
	for _, category := range model.NodeCategories() {
		key := model.NodeKey(category, name)
		if err := s.opts.Authorizer.Authorize(id, key); err != nil {
			refused = err
			continue
		}
		searched++
		if res, ok := s.store.Get(key); ok {
			writeJSON(w, http.StatusOK, res)
			return
		}
	}
	if searched == 0 {
		s.allowed(w, r, refused)
		return
	}
	writeError(w, http.StatusNotFound, ReasonNotFound, "node %q is not published", name)
}

// serveGetDevice returns one device, with only the requested properties if ParamProperty is
// given. A requested property the device does not have is an error, not an empty answer.
func (s *Server) serveGetDevice(w http.ResponseWriter, r *http.Request) {
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	key := model.DeviceKey(namespace, name)
	if !s.authorize(w, r, key) {
		return
	}
	res, ok := s.store.Get(key)
	if !ok {
		writeError(w, http.StatusNotFound, ReasonNotFound, "device %s/%s is not published", namespace, name)
		return
	}
	wanted := r.URL.Query()[ParamProperty]
	if len(wanted) == 0 {
		writeJSON(w, http.StatusOK, res)
		return
	}
	spec, ok := res.Spec.(*model.DeviceSpec)
	if !ok {
		writeError(w, http.StatusInternalServerError, ReasonInternal, "device %s/%s has an unexpected spec", namespace, name)
		return
	}
	filtered, missing := selectProperties(spec, wanted)
	if len(missing) > 0 {
		writeError(w, http.StatusNotFound, ReasonNotFound, "device %s/%s has no property %s", namespace, name, strings.Join(missing, ", "))
		return
	}
	res.Spec = filtered
	writeJSON(w, http.StatusOK, res)
}

// selectProperties returns a copy of spec with only the wanted properties, in spec order, and
// the wanted names spec does not have. spec is not modified: it is shared with the store.
func selectProperties(spec *model.DeviceSpec, wanted []string) (*model.DeviceSpec, []string) {
	want := make(map[string]bool, len(wanted))
	for _, name := range wanted {
		want[name] = false
	}
	out := *spec
	out.Properties = make([]model.DeviceProperty, 0, len(want))
	for _, p := range spec.Properties {
		if _, ok := want[p.Name]; ok {
			out.Properties = append(out.Properties, p)
			want[p.Name] = true
		}
	}
	var missing []string
	for _, name := range wanted {
		if found, ok := want[name]; ok && !found {
			missing = append(missing, name)
			delete(want, name) // report each name once
		}
	}
	return &out, missing
}

func isNodeCategory(category string) bool {
	for _, c := range model.NodeCategories() {
		if string(c) == category {
			return true
		}
	}
	return false
}
