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
	"errors"
	"fmt"
	"strings"
)

const (
	// KeySeparator separates the segments of a key.
	KeySeparator = "/"
	// NodesPrefix is the first key segment of every node.
	NodesPrefix = "nodes"
	// DevicesPrefix is the first key segment of every device.
	DevicesPrefix = "devices"
)

// NodeKey returns the key of a node: "nodes/<category>/<name>".
func NodeKey(category NodeCategory, name string) string {
	return strings.Join([]string{NodesPrefix, string(category), name}, KeySeparator)
}

// DeviceKey returns the key of a device: "devices/<namespace>/<name>".
func DeviceKey(namespace, name string) string {
	return strings.Join([]string{DevicesPrefix, namespace, name}, KeySeparator)
}

// ValidateKey checks that a key has at least two segments, none of them empty, and no
// wildcard characters.
func ValidateKey(key string) error {
	if key == "" {
		return errors.New("resource key is empty")
	}
	if strings.ContainsAny(key, "*?#$") {
		return fmt.Errorf("resource key %q contains a wildcard character", key)
	}
	segments := strings.Split(key, KeySeparator)
	if len(segments) < 2 {
		return fmt.Errorf("resource key %q has fewer than two segments", key)
	}
	for _, s := range segments {
		if s == "" {
			return fmt.Errorf("resource key %q has an empty segment", key)
		}
	}
	return nil
}

// MatchesPrefix reports whether key is selected by prefix. The empty prefix selects every key.
func MatchesPrefix(key, prefix string) bool {
	return strings.HasPrefix(key, prefix)
}
