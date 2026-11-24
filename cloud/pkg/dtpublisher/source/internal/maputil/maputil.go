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

// Package maputil holds the map helpers shared by the sources.
package maputil

// Copy returns a copy of in, or nil if in is empty.
func Copy(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Select returns the entries of in whose keys are listed in keys, or nil if there are none.
func Select(in map[string]string, keys []string) map[string]string {
	var out map[string]string
	for _, k := range keys {
		v, ok := in[k]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(keys))
		}
		out[k] = v
	}
	return out
}
