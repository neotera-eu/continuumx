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

// Package access decides which consumer may read which published resources.
//
// The consumers of I-4 and their access rights are an open decision (brief section 5, item 2;
// OPN-06). Access is an Authorizer of identity and key prefix, separate from how an identity
// is established (package security, OPN-11). The PROVISIONAL Authorizers are:
//   - AllowAll: every consumer the transport accepted, the default when no consumers are
//     configured.
//   - Static: a configured list of consumers, each with the key prefixes it may read.
//
// No consumer is built in.
package access

import (
	"errors"
	"fmt"
	"strings"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
)

var (
	// ErrUnauthenticated is returned for a consumer without an identity where one is needed.
	ErrUnauthenticated = errors.New("consumer is not authenticated")
	// ErrForbidden is returned for a consumer that may not read what it asked for.
	ErrForbidden = errors.New("consumer is not allowed")
)

// Identity is who a consumer is, as established by the transport.
type Identity struct {
	// Authenticated is set when the transport verified the identity, for example a client
	// certificate signed by the configured CA.
	Authenticated bool
	// Name is the verified name, for example the certificate common name.
	Name string
}

// Anonymous is the identity of a consumer that presented none.
var Anonymous = Identity{}

func (id Identity) String() string {
	if !id.Authenticated {
		return "anonymous"
	}
	return fmt.Sprintf("%q", id.Name)
}

// Authorizer decides what a consumer may read.
type Authorizer interface {
	// Admit returns nil if id may use the publisher at all, for example read its capabilities.
	Admit(id Identity) error
	// Authorize returns nil if id may read every resource under prefix: a watch prefix, a
	// list prefix or a single key. It wraps ErrUnauthenticated or ErrForbidden otherwise.
	Authorize(id Identity, prefix string) error
}

// AllowAll admits every consumer to everything. It leaves access to the transport's own
// checks, for example mutual TLS.
type AllowAll struct{}

var _ Authorizer = AllowAll{}

// Admit always returns nil.
func (AllowAll) Admit(Identity) error { return nil }

// Authorize always returns nil.
func (AllowAll) Authorize(Identity, string) error { return nil }

// Consumer is one configured consumer.
type Consumer struct {
	// Name is the identity name, for example the common name of its client certificate.
	Name string `json:"name"`
	// Prefixes are the key prefixes the consumer may read. Each is empty (everything) or ends
	// with the key separator, so it covers whole key segments: "devices/" or
	// "devices/default/", not "devices/def".
	Prefixes []string `json:"prefixes"`
}

// Static admits the configured consumers only, each to its prefixes.
type Static struct {
	consumers map[string][]string
}

var _ Authorizer = (*Static)(nil)

// NewStatic validates consumers and returns their Authorizer. At least one consumer is needed:
// with none, use AllowAll.
func NewStatic(consumers []Consumer) (*Static, error) {
	if len(consumers) == 0 {
		return nil, errors.New("access: no consumers configured")
	}
	s := &Static{consumers: make(map[string][]string, len(consumers))}
	for i, c := range consumers {
		if c.Name == "" {
			return nil, fmt.Errorf("access: consumer %d has no name", i)
		}
		if _, dup := s.consumers[c.Name]; dup {
			return nil, fmt.Errorf("access: consumer %q is configured twice", c.Name)
		}
		if len(c.Prefixes) == 0 {
			return nil, fmt.Errorf("access: consumer %q has no prefixes", c.Name)
		}
		for _, p := range c.Prefixes {
			if err := validatePrefix(p); err != nil {
				return nil, fmt.Errorf("access: consumer %q: %w", c.Name, err)
			}
		}
		s.consumers[c.Name] = append([]string(nil), c.Prefixes...)
	}
	return s, nil
}

func validatePrefix(p string) error {
	if p == "" {
		return nil
	}
	if strings.ContainsAny(p, "*?#$") {
		return fmt.Errorf("prefix %q contains a wildcard character", p)
	}
	if !strings.HasSuffix(p, model.KeySeparator) {
		return fmt.Errorf("prefix %q must end with %q", p, model.KeySeparator)
	}
	if strings.Contains(p, model.KeySeparator+model.KeySeparator) || strings.HasPrefix(p, model.KeySeparator) {
		return fmt.Errorf("prefix %q has an empty segment", p)
	}
	return nil
}

// Admit returns nil for a configured consumer.
func (s *Static) Admit(id Identity) error {
	_, err := s.lookup(id)
	return err
}

// Authorize returns nil if prefix starts with one of the consumer's prefixes. Every key a
// stream or list on prefix can select then lies under that allowed prefix. A request broader
// than every allowed prefix is refused, not narrowed: the consumer asks for what it may read.
// Because allowed prefixes end with the separator, the finest grant is a device namespace or
// a node category, not a single key.
func (s *Static) Authorize(id Identity, prefix string) error {
	allowed, err := s.lookup(id)
	if err != nil {
		return err
	}
	for _, a := range allowed {
		if model.MatchesPrefix(prefix, a) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s may not read %q (allowed: %q)", ErrForbidden, id, prefix, allowed)
}

func (s *Static) lookup(id Identity) ([]string, error) {
	if !id.Authenticated {
		return nil, fmt.Errorf("%w: a client certificate is required", ErrUnauthenticated)
	}
	allowed, ok := s.consumers[id.Name]
	if !ok {
		return nil, fmt.Errorf("%w: %s is not a configured consumer", ErrForbidden, id)
	}
	return allowed, nil
}
