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

// Package client is a Go client of the PROVISIONAL httpsse I-4 transport. It is used by
// "dtpublisher ctl" and by tests; a consumer may use it, but the transport may be replaced
// once the I-4 transport is agreed (OPN-04).
//
// Resources come back with a typed Spec: *model.NodeSpec for nodes, *model.DeviceSpec for
// devices, and json.RawMessage for a kind this client does not know.
package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

var (
	// ErrNotFound matches an APIError for a resource or property that is not published.
	ErrNotFound = errors.New("not found")
	// ErrQueryDisabled matches an APIError from a publisher that does not serve list and get.
	ErrQueryDisabled = errors.New("query operations are disabled on the publisher")
	// ErrUnauthenticated matches an APIError for a request without a verified identity.
	ErrUnauthenticated = errors.New("not authenticated")
	// ErrForbidden matches an APIError for a request the consumer is not allowed to make.
	ErrForbidden = errors.New("forbidden")
)

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 64 * 1024

// APIError is an error response of the publisher.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Reason is the machine-readable reason, for example httpsse.ReasonNotFound.
	Reason string
	// Message is the human-readable description.
	Message string
}

func (e *APIError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("publisher answered %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("publisher answered %d %s: %s", e.StatusCode, e.Reason, e.Message)
}

// Is makes errors.Is match ErrNotFound, ErrQueryDisabled, ErrUnauthenticated and
// ErrForbidden by reason.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Reason == httpsse.ReasonNotFound
	case ErrQueryDisabled:
		return e.Reason == httpsse.ReasonQueryDisabled
	case ErrUnauthenticated:
		return e.Reason == httpsse.ReasonUnauthenticated
	case ErrForbidden:
		return e.Reason == httpsse.ReasonForbidden
	}
	return false
}

// Client talks to one publisher.
type Client struct {
	base string
	http *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client. It must not have a Timeout, which would cut watch
// streams; bound single calls with their context. For TLS, WithTLS is simpler.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.http = hc
	}
}

// WithTLS uses an HTTP client with cfg, for example from security.ClientConfig.TLSConfig.
func WithTLS(cfg *tls.Config) Option {
	return func(c *Client) {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = cfg
		c.http = &http.Client{Transport: tr}
	}
}

// New creates a client of the publisher at baseURL, for example "https://dtpublisher:8443".
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("publisher URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("publisher URL %q: want http(s)://host[:port]", baseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("publisher URL %q: no query or fragment allowed", baseURL)
	}
	c := &Client{base: strings.TrimSuffix(u.String(), "/"), http: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Capabilities returns what the publisher publishes and which operations it serves.
func (c *Client) Capabilities(ctx context.Context) (transport.Capabilities, error) {
	var caps transport.Capabilities
	err := c.getJSON(ctx, httpsse.BasePath, nil, &caps)
	return caps, err
}

// List is the answer of a list operation, at Cursor. Watching from Cursor continues it.
type List struct {
	httpsse.Cursor
	// Items are the matching resources, sorted by key.
	Items []model.Resource
}

// ListNodesOptions filter ListNodes.
type ListNodesOptions struct {
	// Category selects one node category; empty selects all.
	Category model.NodeCategory
}

// ListNodes lists the published nodes. Opt-in on the publisher.
func (c *Client) ListNodes(ctx context.Context, opts ListNodesOptions) (*List, error) {
	q := url.Values{}
	if opts.Category != "" {
		q.Set(httpsse.ParamCategory, string(opts.Category))
	}
	return c.list(ctx, httpsse.NodesPath, q)
}

// ListDevicesOptions filter ListDevices.
type ListDevicesOptions struct {
	// Namespace selects one namespace; empty selects all.
	Namespace string
	// Node selects the devices hosted by one node; empty selects all.
	Node string
}

// ListDevices lists the published devices. Opt-in on the publisher.
func (c *Client) ListDevices(ctx context.Context, opts ListDevicesOptions) (*List, error) {
	q := url.Values{}
	if opts.Namespace != "" {
		q.Set(httpsse.ParamNamespace, opts.Namespace)
	}
	if opts.Node != "" {
		q.Set(httpsse.ParamNode, opts.Node)
	}
	return c.list(ctx, httpsse.DevicesPath, q)
}

// GetNode returns one node by name. Opt-in on the publisher.
func (c *Client) GetNode(ctx context.Context, name string) (*model.Resource, error) {
	if name == "" {
		return nil, errors.New("node name is empty")
	}
	return c.get(ctx, httpsse.NodesPath+"/"+url.PathEscape(name), nil)
}

// GetDevice returns one device. With properties, only those properties are returned, and a
// property the device does not have is ErrNotFound. Opt-in on the publisher.
func (c *Client) GetDevice(ctx context.Context, namespace, name string, properties ...string) (*model.Resource, error) {
	if namespace == "" || name == "" {
		return nil, errors.New("device namespace and name must be set")
	}
	q := url.Values{}
	for _, p := range properties {
		q.Add(httpsse.ParamProperty, p)
	}
	return c.get(ctx, httpsse.DevicesPath+"/"+url.PathEscape(namespace)+"/"+url.PathEscape(name), q)
}

func (c *Client) list(ctx context.Context, path string, q url.Values) (*List, error) {
	var raw struct {
		httpsse.Cursor
		Items []json.RawMessage `json:"items"`
	}
	if err := c.getJSON(ctx, path, q, &raw); err != nil {
		return nil, err
	}
	out := &List{Cursor: raw.Cursor, Items: make([]model.Resource, 0, len(raw.Items))}
	for _, item := range raw.Items {
		r, err := DecodeResource(item)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, r)
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (*model.Resource, error) {
	var raw json.RawMessage
	if err := c.getJSON(ctx, path, q, &raw); err != nil {
		return nil, err
	}
	r, err := DecodeResource(raw)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// do sends a GET and returns the response if its status is 200, an *APIError otherwise.
func (c *Client) do(ctx context.Context, path string, q url.Values, accept string) (*http.Response, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	return nil, readAPIError(resp)
}

func readAPIError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	apiErr := &APIError{StatusCode: resp.StatusCode}
	var body httpsse.ErrorBody
	if json.Unmarshal(data, &body) == nil && (body.Reason != "" || body.Error != "") {
		apiErr.Reason, apiErr.Message = body.Reason, body.Error
	} else {
		apiErr.Message = strings.TrimSpace(string(data))
	}
	return apiErr
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out interface{}) error {
	resp, err := c.do(ctx, path, q, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// DecodeResource decodes a published resource with a typed Spec: *model.NodeSpec,
// *model.DeviceSpec, or json.RawMessage for an unknown kind.
func DecodeResource(data []byte) (model.Resource, error) {
	var raw struct {
		model.Resource
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return model.Resource{}, fmt.Errorf("decode resource: %w", err)
	}
	r := raw.Resource
	var spec interface{}
	switch r.Kind {
	case model.KindNode:
		spec = &model.NodeSpec{}
	case model.KindDevice:
		spec = &model.DeviceSpec{}
	default:
		r.Spec = raw.Spec
		return r, nil
	}
	if err := json.Unmarshal(raw.Spec, spec); err != nil {
		return model.Resource{}, fmt.Errorf("decode %s spec of %q: %w", r.Kind, r.Key, err)
	}
	r.Spec = spec
	return r, nil
}
