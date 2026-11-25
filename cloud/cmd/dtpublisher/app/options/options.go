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

// Package options is the configuration of "dtpublisher serve": a YAML file, overridden by
// the flags that are set explicitly.
package options

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/zenoh"
)

// Defaults.
const (
	DefaultTransport     = httpsse.Name
	DefaultListenAddress = ":8443"
	DefaultHealthAddress = ":8080"
)

// Config is the configuration file of "dtpublisher serve". Every field is optional.
type Config struct {
	// Kubeconfig is the kubeconfig file. Empty, with Master empty, uses the in-cluster config.
	Kubeconfig string `json:"kubeconfig,omitempty"`
	// Master overrides the API server address of the kubeconfig.
	Master string `json:"master,omitempty"`
	// Sources selects and configures what is published.
	Sources Sources `json:"sources"`
	// MinPublishInterval is the minimum interval between two published changes of one key.
	// Zero publishes every change.
	MinPublishInterval metav1.Duration `json:"minPublishInterval"`
	// Transport is the I-4 transport.
	Transport Transport `json:"transport"`
	// Query enables the opt-in list and get operations.
	Query Query `json:"query"`
	// Security is the transport security.
	Security Security `json:"security"`
	// Access lists the consumers. Empty allows every consumer the transport accepted.
	Access Access `json:"access"`
	// HealthAddress serves /healthz, /readyz and /metrics over plain HTTP. Not part of I-4.
	HealthAddress string `json:"healthAddress"`
}

// Sources configures the resource sources.
type Sources struct {
	Nodes   SourceConfig `json:"nodes"`
	Devices SourceConfig `json:"devices"`
}

// SourceConfig configures one source.
type SourceConfig struct {
	// Enabled publishes the source. Default true.
	Enabled *bool `json:"enabled,omitempty"`
	// AnnotationKeys lists the annotations to publish. None by default.
	AnnotationKeys []string `json:"annotationKeys,omitempty"`
}

// IsEnabled returns Enabled, true if unset.
func (s SourceConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// Transport configures the I-4 transport.
type Transport struct {
	// Type is the transport name: "httpsse" (default) or "zenoh" (not implemented).
	Type    string  `json:"type"`
	HTTPSSE HTTPSSE `json:"httpsse"`
}

// HTTPSSE configures the httpsse transport.
type HTTPSSE struct {
	// Address is the listen address. Default ":8443".
	Address string `json:"address"`
	// HeartbeatInterval is the HEARTBEAT interval on a quiet stream. Zero uses the default.
	HeartbeatInterval metav1.Duration `json:"heartbeatInterval"`
	// BufferSize is the event buffer per subscriber. Zero uses the default.
	BufferSize int `json:"bufferSize"`
	// WriteTimeout bounds one write to a subscriber. Zero uses the default.
	WriteTimeout metav1.Duration `json:"writeTimeout"`
}

// Query configures the opt-in list and get operations.
type Query struct {
	// Enabled serves list and get. Default false.
	Enabled bool `json:"enabled"`
}

// Security configures the transport security.
type Security struct {
	// InsecureDev serves plain HTTP, without TLS. For development playgrounds only.
	InsecureDev bool `json:"insecureDev"`
	// TLS is the server TLS configuration, required unless InsecureDev.
	TLS security.ServerConfig `json:"tls"`
}

// Access configures consumer access.
type Access struct {
	// Consumers lists the consumers and what each may read.
	Consumers []access.Consumer `json:"consumers,omitempty"`
}

// NewConfig returns the default configuration.
func NewConfig() *Config {
	return &Config{
		Transport:     Transport{Type: DefaultTransport, HTTPSSE: HTTPSSE{Address: DefaultListenAddress}},
		HealthAddress: DefaultHealthAddress,
	}
}

// LoadFile reads a configuration file over the defaults. Unknown fields are an error.
func LoadFile(path string) (*Config, error) {
	cfg := NewConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.UnmarshalStrict(data, cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	var errs []error
	if !c.Sources.Nodes.IsEnabled() && !c.Sources.Devices.IsEnabled() {
		errs = append(errs, errors.New("sources: at least one of nodes and devices must be enabled"))
	}
	if c.MinPublishInterval.Duration < 0 {
		errs = append(errs, errors.New("minPublishInterval must not be negative"))
	}
	switch c.Transport.Type {
	case httpsse.Name:
		if c.Transport.HTTPSSE.Address == "" {
			errs = append(errs, errors.New("transport.httpsse.address must be set"))
		}
		if c.Transport.HTTPSSE.HeartbeatInterval.Duration < 0 || c.Transport.HTTPSSE.WriteTimeout.Duration < 0 || c.Transport.HTTPSSE.BufferSize < 0 {
			errs = append(errs, errors.New("transport.httpsse: durations and buffer size must not be negative"))
		}
	case zenoh.Name:
		// Registered and refused at start: not implemented.
	default:
		errs = append(errs, fmt.Errorf("transport.type %q is unknown, valid: %q, %q", c.Transport.Type, httpsse.Name, zenoh.Name))
	}
	if c.HealthAddress == "" {
		errs = append(errs, errors.New("healthAddress must be set"))
	}

	tls := c.Security.TLS
	tlsSet := tls != (security.ServerConfig{})
	switch {
	case c.Security.InsecureDev && tlsSet:
		errs = append(errs, errors.New("security: insecureDev and tls are mutually exclusive"))
	case !c.Security.InsecureDev:
		if err := tls.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%w (TLS is required; plain HTTP needs insecureDev)", err))
		}
	}
	if len(c.Access.Consumers) > 0 {
		if _, err := access.NewStatic(c.Access.Consumers); err != nil {
			errs = append(errs, err)
		}
		// A consumer list needs verified client certificates, or every consumer is refused.
		if c.Security.InsecureDev || tls.ClientAuth == "" || tls.ClientAuth == security.ClientAuthNone {
			errs = append(errs, fmt.Errorf("access.consumers needs client certificates: set security.tls.clientAuth to %q or %q", security.ClientAuthOptional, security.ClientAuthRequire))
		}
	}
	return errors.Join(errs...)
}

// Flags are the flags of "dtpublisher serve". Each one set explicitly overrides the file.
type Flags struct {
	ConfigFile string

	kubeconfig, master                 string
	listenAddress, healthAddress       string
	transport                          string
	enableQuery, insecureDev           bool
	certFile, keyFile, clientCA        string
	clientAuth                         string
	minPublishInterval                 time.Duration
	nodeAnnotations, deviceAnnotations []string

	fs *pflag.FlagSet
}

// AddFlags registers the flags on fs.
func (f *Flags) AddFlags(fs *pflag.FlagSet) {
	f.fs = fs
	fs.StringVar(&f.ConfigFile, "config", "", "Configuration file (YAML). Flags set explicitly override it.")
	fs.StringVar(&f.kubeconfig, "kubeconfig", "", "Kubeconfig file. Empty, with --master empty, uses the in-cluster configuration.")
	fs.StringVar(&f.master, "master", "", "Kubernetes API server address (overrides the kubeconfig).")
	fs.StringVar(&f.transport, "transport", DefaultTransport, `I-4 transport: "httpsse" or "zenoh" (not implemented, to be discussed with ZSC and DLR).`)
	fs.StringVar(&f.listenAddress, "listen-address", DefaultListenAddress, "Listen address of the httpsse transport.")
	fs.StringVar(&f.healthAddress, "health-address", DefaultHealthAddress, "Listen address of /healthz, /readyz and /metrics (plain HTTP, not part of I-4).")
	fs.BoolVar(&f.enableQuery, "enable-query", false, "Serve the opt-in list and get operations.")
	fs.BoolVar(&f.insecureDev, "insecure-dev", false, "Serve plain HTTP without TLS. For development playgrounds only.")
	fs.StringVar(&f.certFile, "tls-cert-file", "", "Server certificate (PEM, with chain).")
	fs.StringVar(&f.keyFile, "tls-private-key-file", "", "Server private key (PEM).")
	fs.StringVar(&f.clientCA, "client-ca-file", "", "CA bundle client certificates are verified against.")
	fs.StringVar(&f.clientAuth, "client-auth", "", `Client certificates: "none" (default), "optional" or "require".`)
	fs.DurationVar(&f.minPublishInterval, "min-publish-interval", 0, "Minimum interval between two published changes of one resource. 0 publishes every change.")
	fs.StringSliceVar(&f.nodeAnnotations, "node-annotations", nil, "Node annotation keys to publish. None by default.")
	fs.StringSliceVar(&f.deviceAnnotations, "device-annotations", nil, "Device annotation keys to publish. None by default.")
}

// Config loads the configuration file, if any, applies the flags set explicitly and
// validates the result.
func (f *Flags) Config() (*Config, error) {
	cfg := NewConfig()
	if f.ConfigFile != "" {
		var err error
		if cfg, err = LoadFile(f.ConfigFile); err != nil {
			return nil, err
		}
	}
	set := func(name string) bool { return f.fs != nil && f.fs.Changed(name) }
	if set("kubeconfig") {
		cfg.Kubeconfig = f.kubeconfig
	}
	if set("master") {
		cfg.Master = f.master
	}
	if set("transport") {
		cfg.Transport.Type = f.transport
	}
	if set("listen-address") {
		cfg.Transport.HTTPSSE.Address = f.listenAddress
	}
	if set("health-address") {
		cfg.HealthAddress = f.healthAddress
	}
	if set("enable-query") {
		cfg.Query.Enabled = f.enableQuery
	}
	if set("insecure-dev") {
		cfg.Security.InsecureDev = f.insecureDev
	}
	if set("tls-cert-file") {
		cfg.Security.TLS.CertFile = f.certFile
	}
	if set("tls-private-key-file") {
		cfg.Security.TLS.KeyFile = f.keyFile
	}
	if set("client-ca-file") {
		cfg.Security.TLS.ClientCAFile = f.clientCA
	}
	if set("client-auth") {
		cfg.Security.TLS.ClientAuth = security.ClientAuth(f.clientAuth)
	}
	if set("min-publish-interval") {
		cfg.MinPublishInterval.Duration = f.minPublishInterval
	}
	if set("node-annotations") {
		cfg.Sources.Nodes.AnnotationKeys = f.nodeAnnotations
	}
	if set("device-annotations") {
		cfg.Sources.Devices.AnnotationKeys = f.deviceAnnotations
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
