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

package options

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
)

const fullConfig = `
kubeconfig: /etc/dtpublisher/kubeconfig
sources:
  nodes:
    annotationKeys: [example.com/zone]
  devices:
    enabled: true
minPublishInterval: 1s
transport:
  type: httpsse
  httpsse:
    address: ":9443"
    heartbeatInterval: 5s
    bufferSize: 64
query:
  enabled: true
security:
  tls:
    certFile: /tls/tls.crt
    keyFile: /tls/tls.key
    clientCAFile: /tls/ca.crt
    clientAuth: require
access:
  consumers:
  - name: consumer-a
    prefixes: ["nodes/", "devices/"]
healthAddress: ":9090"
`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dtpublisher.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestDefaults(t *testing.T) {
	cfg := NewConfig()
	assert.Equal(t, "httpsse", cfg.Transport.Type)
	assert.Equal(t, ":8443", cfg.Transport.HTTPSSE.Address)
	assert.Equal(t, ":8080", cfg.HealthAddress)
	assert.True(t, cfg.Sources.Nodes.IsEnabled())
	assert.True(t, cfg.Sources.Devices.IsEnabled())
	assert.False(t, cfg.Query.Enabled, "list and get are opt-in")
	assert.Zero(t, cfg.MinPublishInterval.Duration)
	assert.ErrorContains(t, cfg.Validate(), "TLS is required", "the defaults alone do not serve plain HTTP")
}

func TestLoadFile(t *testing.T) {
	cfg, err := LoadFile(writeFile(t, fullConfig))
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())

	assert.Equal(t, "/etc/dtpublisher/kubeconfig", cfg.Kubeconfig)
	assert.Equal(t, []string{"example.com/zone"}, cfg.Sources.Nodes.AnnotationKeys)
	assert.Equal(t, time.Second, cfg.MinPublishInterval.Duration)
	assert.Equal(t, ":9443", cfg.Transport.HTTPSSE.Address)
	assert.Equal(t, 5*time.Second, cfg.Transport.HTTPSSE.HeartbeatInterval.Duration)
	assert.Equal(t, 64, cfg.Transport.HTTPSSE.BufferSize)
	assert.True(t, cfg.Query.Enabled)
	assert.Equal(t, security.ClientAuthRequire, cfg.Security.TLS.ClientAuth)
	assert.Equal(t, []access.Consumer{{Name: "consumer-a", Prefixes: []string{"nodes/", "devices/"}}}, cfg.Access.Consumers)
	assert.Equal(t, ":9090", cfg.HealthAddress)

	partial, err := LoadFile(writeFile(t, "query:\n  enabled: true\n"))
	require.NoError(t, err)
	assert.Equal(t, ":8443", partial.Transport.HTTPSSE.Address, "defaults kept for fields not in the file")

	_, err = LoadFile(writeFile(t, "qurey:\n  enabled: true\n"))
	assert.ErrorContains(t, err, "qurey", "unknown fields are an error")
	_, err = LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestFlagsOverrideFile(t *testing.T) {
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	f := &Flags{}
	f.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{
		"--config", writeFile(t, fullConfig),
		"--health-address", "127.0.0.1:0",
		"--min-publish-interval", "250ms",
		"--device-annotations", "a,b",
		"--client-auth", "optional",
	}))
	cfg, err := f.Config()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:0", cfg.HealthAddress)
	assert.Equal(t, 250*time.Millisecond, cfg.MinPublishInterval.Duration)
	assert.Equal(t, []string{"a", "b"}, cfg.Sources.Devices.AnnotationKeys)
	assert.Equal(t, security.ClientAuthOptional, cfg.Security.TLS.ClientAuth)
	assert.Equal(t, ":9443", cfg.Transport.HTTPSSE.Address, "flags not set keep the file value, not the flag default")
	assert.True(t, cfg.Query.Enabled)
}

func TestFlagsWithoutFile(t *testing.T) {
	fs := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	f := &Flags{}
	f.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{"--insecure-dev", "--enable-query", "--listen-address", "127.0.0.1:0", "--transport", "zenoh"}))
	cfg, err := f.Config()
	require.NoError(t, err, "zenoh passes validation; the publisher refuses it at start")
	assert.True(t, cfg.Security.InsecureDev)
	assert.True(t, cfg.Query.Enabled)
	assert.Equal(t, "zenoh", cfg.Transport.Type)

	fs = pflag.NewFlagSet("serve", pflag.ContinueOnError)
	f = &Flags{}
	f.AddFlags(fs)
	require.NoError(t, fs.Parse(nil))
	_, err = f.Config()
	assert.ErrorContains(t, err, "TLS is required")
}

func TestValidate(t *testing.T) {
	no := false
	tlsOnly := security.ServerConfig{CertFile: "/tls/tls.crt", KeyFile: "/tls/tls.key"}
	mtls := security.ServerConfig{CertFile: "/tls/tls.crt", KeyFile: "/tls/tls.key", ClientCAFile: "/tls/ca.crt", ClientAuth: security.ClientAuthOptional}
	consumers := []access.Consumer{{Name: "consumer-a", Prefixes: []string{"devices/"}}}

	for name, tc := range map[string]struct {
		change func(*Config)
		want   string
	}{
		"insecure and tls":        {func(c *Config) { c.Security = Security{InsecureDev: true, TLS: tlsOnly} }, "mutually exclusive"},
		"no tls":                  {func(c *Config) {}, "TLS is required"},
		"consumers on plain HTTP": {func(c *Config) { c.Security.InsecureDev = true; c.Access.Consumers = consumers }, "needs client certificates"},
		"consumers without mTLS":  {func(c *Config) { c.Security.TLS = tlsOnly; c.Access.Consumers = consumers }, "needs client certificates"},
		"bad consumer prefix": {func(c *Config) {
			c.Security.TLS = mtls
			c.Access.Consumers = []access.Consumer{{Name: "a", Prefixes: []string{"dev"}}}
		}, "must end with"},
		"unknown transport": {func(c *Config) { c.Security.InsecureDev = true; c.Transport.Type = "mqtt" }, "unknown"},
		"no source": {func(c *Config) {
			c.Security.InsecureDev = true
			c.Sources.Nodes.Enabled = &no
			c.Sources.Devices.Enabled = &no
		}, "at least one"},
		"negative interval":          {func(c *Config) { c.Security.InsecureDev = true; c.MinPublishInterval.Duration = -time.Second }, "negative"},
		"no listen address":          {func(c *Config) { c.Security.InsecureDev = true; c.Transport.HTTPSSE.Address = "" }, "address"},
		"no health address":          {func(c *Config) { c.Security.InsecureDev = true; c.HealthAddress = "" }, "healthAddress"},
		"negative transport setting": {func(c *Config) { c.Security.InsecureDev = true; c.Transport.HTTPSSE.BufferSize = -1 }, "negative"},
	} {
		cfg := NewConfig()
		tc.change(cfg)
		assert.ErrorContains(t, cfg.Validate(), tc.want, name)
	}

	ok := NewConfig()
	ok.Security.TLS = mtls
	ok.Access.Consumers = consumers
	assert.NoError(t, ok.Validate())
}
