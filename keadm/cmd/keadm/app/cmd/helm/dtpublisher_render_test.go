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

package helm

// Render tests of the Device Twins Resources Publisher (UP2DATE4SDV CID 93) part of the
// cloudcore chart, and the check that the raw manifests in build/dtpublisher match it.

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/neotera-eu/continuumx/cloud/cmd/dtpublisher/app/options"
	"github.com/neotera-eu/continuumx/common/constants"
	types "github.com/neotera-eu/continuumx/keadm/cmd/keadm/app/cmd/common"
	kecharts "github.com/neotera-eu/continuumx/manifests"
)

type object = map[string]interface{}

// renderChart renders the built-in cloudcore chart as cxadm does, with the given sets.
func renderChart(sets ...string) (string, error) {
	vals, err := (&Options{Values: sets}).MergeValues()
	if err != nil {
		return "", err
	}
	r := NewGenericRenderer(kecharts.BuiltinOrDir(""), path.Join(dirCharts, cloudCoreHelmComponent),
		cloudCoreHelmComponent, constants.SystemNamespace, vals, true)
	if err := r.LoadChart(); err != nil {
		return "", err
	}
	return r.RenderManifest()
}

// dtpublisherObjects returns the rendered objects named dtpublisher*, keyed by "Kind/name".
func dtpublisherObjects(t *testing.T, manifest string) map[string]object {
	t.Helper()
	out := map[string]object{}
	for _, doc := range strings.Split(manifest, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var o object
		require.NoError(t, yaml.Unmarshal([]byte(doc), &o), doc)
		if o == nil {
			continue
		}
		name, _ := get(o, "metadata", "name").(string)
		if strings.HasPrefix(name, "dtpublisher") {
			out[o["kind"].(string)+"/"+name] = o
		}
	}
	return out
}

func get(o interface{}, keys ...string) interface{} {
	for _, k := range keys {
		m, ok := o.(map[string]interface{})
		if !ok {
			return nil
		}
		o = m[k]
	}
	return o
}

func keysOf(m map[string]object) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func container(t *testing.T, objs map[string]object) object {
	t.Helper()
	containers := get(objs["Deployment/dtpublisher"], "spec", "template", "spec", "containers").([]interface{})
	require.Len(t, containers, 1)
	return containers[0].(object)
}

func volumeNames(objs map[string]object) []string {
	var names []string
	for _, v := range get(objs["Deployment/dtpublisher"], "spec", "template", "spec", "volumes").([]interface{}) {
		names = append(names, v.(object)["name"].(string))
	}
	return names
}

// publisherConfig loads the rendered config.yaml with the publisher's own loader.
func publisherConfig(t *testing.T, objs map[string]object) *options.Config {
	t.Helper()
	data, ok := get(objs["ConfigMap/dtpublisher"], "data", "config.yaml").(string)
	require.True(t, ok, "config.yaml in the ConfigMap")
	file := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte(data), 0o600))
	cfg, err := options.LoadFile(file)
	require.NoError(t, err, data)
	require.NoError(t, cfg.Validate(), data)
	return cfg
}

func TestDTPublisherDisabledByDefault(t *testing.T) {
	manifest, err := renderChart()
	require.NoError(t, err)
	assert.Empty(t, dtpublisherObjects(t, manifest))
	assert.NotContains(t, manifest, "dtpublisher")
}

func TestDTPublisherEnabledDefaults(t *testing.T) {
	manifest, err := renderChart("dtPublisher.enable=true")
	require.NoError(t, err)
	objs := dtpublisherObjects(t, manifest)
	assert.Equal(t, []string{
		"ClusterRole/dtpublisher", "ClusterRoleBinding/dtpublisher", "ConfigMap/dtpublisher",
		"Deployment/dtpublisher", "Secret/dtpublisher-tls", "Service/dtpublisher", "ServiceAccount/dtpublisher",
	}, keysOf(objs))

	for key, o := range objs {
		if !strings.HasPrefix(key, "ClusterRole") {
			assert.Equal(t, "continuumx", get(o, "metadata", "namespace"), key)
		}
	}

	c := container(t, objs)
	assert.Equal(t, "neotera/dtpublisher:v1.23.0", c["image"])
	assert.Equal(t, []interface{}{"serve", "--config=/etc/dtpublisher/config/config.yaml", "--v=2"}, c["args"])
	assert.Equal(t, []string{"config", "tls"}, volumeNames(objs), "TLS on, no client CA")
	assert.Equal(t, "dtpublisher-tls", get(get(objs["Deployment/dtpublisher"], "spec", "template", "spec", "volumes").([]interface{})[1], "secret", "secretName"))
	assert.Equal(t, int64(1), toInt(get(objs["Deployment/dtpublisher"], "spec", "replicas")))
	assert.Equal(t, true, get(objs["Deployment/dtpublisher"], "spec", "template", "spec", "securityContext", "runAsNonRoot"))

	secret := objs["Secret/dtpublisher-tls"]
	assert.Equal(t, "kubernetes.io/tls", secret["type"])
	assert.Equal(t, "pre-install", get(secret, "metadata", "annotations", "helm.sh/hook"))
	for _, k := range []string{"tls.crt", "tls.key", "ca.crt"} {
		assert.NotEmpty(t, get(secret, "data", k), k)
	}

	rules := get(objs["ClusterRole/dtpublisher"], "rules").([]interface{})
	for _, r := range rules {
		assert.Equal(t, []interface{}{"get", "list", "watch"}, get(r, "verbs"), "read-only")
	}

	svc := objs["Service/dtpublisher"]
	assert.Equal(t, "ClusterIP", get(svc, "spec", "type"))
	ports := get(svc, "spec", "ports").([]interface{})
	require.Len(t, ports, 1, "only I-4; health and metrics are not exposed")
	assert.Equal(t, int64(8443), toInt(get(ports[0], "port")))

	cfg := publisherConfig(t, objs)
	assert.False(t, cfg.Query.Enabled, "query is opt-in")
	assert.False(t, cfg.Security.InsecureDev)
	assert.Equal(t, "/etc/dtpublisher/tls/tls.crt", cfg.Security.TLS.CertFile)
	assert.Equal(t, "httpsse", cfg.Transport.Type)
	assert.True(t, cfg.Sources.Nodes.IsEnabled())
	assert.True(t, cfg.Sources.Devices.IsEnabled())
	assert.Empty(t, cfg.Access.Consumers)
}

func TestDTPublisherImageFromCxadmFlags(t *testing.T) {
	opts := &types.CloudInitUpdateBase{ImageRepository: "registry.example.com/neotera/"}
	opts.Sets = []string{"dtPublisher.enable=true"}
	appendDefaultSets("v1.24.0", "", opts)
	manifest, err := renderChart(opts.Sets...)
	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/neotera/dtpublisher:v1.24.0", container(t, dtpublisherObjects(t, manifest))["image"])
}

func TestDTPublisherPlayground(t *testing.T) {
	manifest, err := renderChart("dtPublisher.enable=true", "dtPublisher.security.insecureDev=true",
		"dtPublisher.query.enabled=true", "dtPublisher.service.type=NodePort", "dtPublisher.service.nodePort=30443",
		"dtPublisher.minPublishInterval=1s", "dtPublisher.sources.nodes.annotationKeys[0]=example.com/zone")
	require.NoError(t, err)
	objs := dtpublisherObjects(t, manifest)
	assert.NotContains(t, objs, "Secret/dtpublisher-tls", "no certificate on plain HTTP")
	assert.Equal(t, []string{"config"}, volumeNames(objs))
	assert.Equal(t, int64(30443), toInt(get(get(objs["Service/dtpublisher"], "spec", "ports").([]interface{})[0], "nodePort")))

	cfg := publisherConfig(t, objs)
	assert.True(t, cfg.Security.InsecureDev)
	assert.True(t, cfg.Query.Enabled)
	assert.Equal(t, "1s", cfg.MinPublishInterval.Duration.String())
	assert.Equal(t, []string{"example.com/zone"}, cfg.Sources.Nodes.AnnotationKeys)
}

func TestDTPublisherMutualTLSAndConsumers(t *testing.T) {
	manifest, err := renderChart("dtPublisher.enable=true",
		"dtPublisher.security.tls.secretName=my-tls", "dtPublisher.security.tls.clientAuth=require",
		"dtPublisher.security.tls.clientCASecretName=my-client-ca",
		"dtPublisher.access.consumers[0].name=consumer-a",
		"dtPublisher.access.consumers[0].prefixes={nodes/,devices/}")
	require.NoError(t, err)
	objs := dtpublisherObjects(t, manifest)
	assert.NotContains(t, objs, "Secret/dtpublisher-tls", "an existing Secret is used")
	assert.Equal(t, []string{"config", "tls", "client-ca"}, volumeNames(objs))

	cfg := publisherConfig(t, objs)
	assert.Equal(t, "require", string(cfg.Security.TLS.ClientAuth))
	assert.Equal(t, "/etc/dtpublisher/client-ca/ca.crt", cfg.Security.TLS.ClientCAFile)
	require.Len(t, cfg.Access.Consumers, 1)
	assert.Equal(t, []string{"nodes/", "devices/"}, cfg.Access.Consumers[0].Prefixes)
}

func TestDTPublisherRejectsInvalidValues(t *testing.T) {
	for want, sets := range map[string][]string{
		"want none, optional or require": {"dtPublisher.security.tls.clientAuth=always"},
		"clientCASecretName is required": {"dtPublisher.security.tls.clientAuth=optional"},
		"needs client certificates":      {"dtPublisher.access.consumers[0].name=a", "dtPublisher.access.consumers[0].prefixes={devices/}"},
		"needs client certificates ":     {"dtPublisher.security.insecureDev=true", "dtPublisher.access.consumers[0].name=a", "dtPublisher.access.consumers[0].prefixes={devices/}"},
	} {
		_, err := renderChart(append([]string{"dtPublisher.enable=true"}, sets...)...)
		assert.ErrorContains(t, err, strings.TrimSpace(want), "%v", sets)
	}
}

// TestDTPublisherRawManifestsMatchChart checks that build/dtpublisher holds the chart's
// objects for dtPublisher.enable=true, apart from the generated Secret and the config checksum.
func TestDTPublisherRawManifestsMatchChart(t *testing.T) {
	manifest, err := renderChart("dtPublisher.enable=true")
	require.NoError(t, err)
	want := dtpublisherObjects(t, manifest)
	delete(want, "Secret/dtpublisher-tls")
	// The checksum is the only pod annotation; the raw manifest has none.
	delete(get(want["Deployment/dtpublisher"], "spec", "template", "metadata").(object), "annotations")

	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "..", "..", "build", "dtpublisher", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	got := map[string]object{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		for key, o := range dtpublisherObjects(t, string(data)) {
			got[key] = o
		}
	}
	assert.Equal(t, keysOf(want), keysOf(got))
	for key := range want {
		assert.Equal(t, want[key], got[key], key)
	}
}

func toInt(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return -1
}
