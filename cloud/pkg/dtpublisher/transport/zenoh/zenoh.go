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

// Package zenoh reserves the name of a Zenoh transport for I-4.
//
// NOT IMPLEMENTED: to be discussed with ZSC and DLR. D3.3 records that ClOrch expects
// communication via the Zenoh protocol, and it is open which side adapts (OPN-04). The Go
// binding, its licence and build needs, the key expressions, the payload encoding and the
// access model are not agreed, so this package defines none of them and adds no dependency.
// Its factory always fails with transport.ErrNotImplemented.
package zenoh

import (
	"fmt"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
)

// Name is the transport name.
const Name = "zenoh"

// Status is the reason the transport is not available.
const Status = "to be discussed with ZSC and DLR"

// Options are the options of the Zenoh transport. None are defined until the adapter is
// agreed.
type Options struct{}

// Factory returns a factory that always fails with transport.ErrNotImplemented.
func Factory(Options) transport.Factory {
	return func(transport.Deps) (transport.Publisher, error) {
		return nil, fmt.Errorf("%w: %s adapter, %s", transport.ErrNotImplemented, Name, Status)
	}
}
