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

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/kubeedge/api/apis/devices/v1beta1"
	crdfake "github.com/kubeedge/api/client/clientset/versioned/fake"
	"github.com/neotera-eu/continuumx/cloud/cmd/dtpublisher/app/options"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/testing/testpki"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
)

const (
	waitTimeout = 5 * time.Second
	tireName    = "automotive-tire-pressure-ecu-fw-001"
)

func testNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

// fakeClients holds the playground's two nodes and a tire-pressure device on u2deb-edge.
func fakeClients() (*kubefake.Clientset, *crdfake.Clientset) {
	kube := kubefake.NewSimpleClientset(
		testNode("u2deb-edge", map[string]string{"node-role.kubernetes.io/edge": ""}),
		testNode("u2deb-cloud", map[string]string{"node-role.kubernetes.io/control-plane": ""}),
	)
	objects := []runtime.Object{
		&v1beta1.DeviceModel{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "tire-model"},
			Spec: v1beta1.DeviceModelSpec{Protocol: "virtual-ethernet", Properties: []v1beta1.ModelProperty{
				{Name: "frontLeftPressure", Type: v1beta1.FLOAT, AccessMode: v1beta1.ReadOnly, Unit: "kPa"},
			}},
		},
		&v1beta1.Device{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: tireName},
			Spec: v1beta1.DeviceSpec{
				DeviceModelRef: &corev1.LocalObjectReference{Name: "tire-model"},
				NodeName:       "u2deb-edge",
				Properties:     []v1beta1.DeviceProperty{{Name: "frontLeftPressure"}},
			},
		},
		&v1beta1.DeviceStatus{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: tireName},
			Status: v1beta1.DeviceStatusStatus{Twins: []v1beta1.Twin{
				{PropertyName: "frontLeftPressure", Reported: v1beta1.TwinProperty{Value: "230.0"}},
			}},
		},
	}
	return kube, crdfake.NewSimpleClientset(objects...)
}

// insecureConfig is a playground configuration on free local ports.
func insecureConfig() *options.Config {
	cfg := options.NewConfig()
	cfg.Security.InsecureDev = true
	cfg.Query.Enabled = true
	cfg.Transport.HTTPSSE.Address = "127.0.0.1:0"
	cfg.HealthAddress = "127.0.0.1:0"
	return cfg
}

// run starts a publisher and stops it at the end of the test. It waits until it is ready.
func run(t *testing.T, cfg *options.Config, clients Clients) *Publisher {
	t.Helper()
	require.NoError(t, cfg.Validate())
	p, err := NewPublisher(cfg, clients)
	require.NoError(t, err)
	assert.False(t, p.Ready(), "not ready before Run")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Error("publisher did not stop")
		}
	})
	require.Eventually(t, func() bool {
		code, _ := httpGet(t, "http://"+p.HealthAddr().String()+"/readyz")
		return code == http.StatusOK
	}, waitTimeout, 20*time.Millisecond, "readyz")
	return p
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// ctl runs "dtpublisher ctl args..." and returns its output.
func ctl(ctx context.Context, args ...string) (string, string, error) {
	cmd := NewDTPublisherCommand()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"ctl"}, args...))
	err := cmd.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func TestServeAndCtl(t *testing.T) {
	kube, crd := fakeClients()
	p := run(t, insecureConfig(), Clients{Kube: kube, CRD: crd})
	server := "http://" + p.I4Addr().String()
	bg := context.Background()

	code, body := httpGet(t, "http://"+p.HealthAddr().String()+"/healthz")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ok\n", body)

	out, _, err := ctl(bg, "--server", server, "capabilities", "-o", "json")
	require.NoError(t, err)
	var caps transport.Capabilities
	require.NoError(t, json.Unmarshal([]byte(out), &caps))
	assert.Equal(t, transport.Operations{Watch: true, List: true, Get: true}, caps.Operations)
	assert.Len(t, caps.Kinds, 2)
	assert.Equal(t, uint64(3), caps.Revision, "two nodes and one device")

	out, _, err = ctl(bg, "--server", server, "list", "nodes")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"NAME", "CATEGORY", "READY", "CPU", "MEMORY", "REVISION"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"u2deb-cloud", "cloud-control-plane", "True", "4", "8Gi"}, strings.Fields(lines[1])[:5])
	assert.Equal(t, []string{"u2deb-edge", "edge-worker", "True", "4", "8Gi"}, strings.Fields(lines[2])[:5])

	out, _, err = ctl(bg, "--server", server, "list", "devices", "--node", "u2deb-edge")
	require.NoError(t, err)
	assert.Contains(t, out, "default    "+tireName+"  u2deb-edge")

	out, _, err = ctl(bg, "--server", server, "get", "device", "default/"+tireName, "--property", "frontLeftPressure")
	require.NoError(t, err)
	assert.Contains(t, out, "key: devices/default/"+tireName)
	assert.Contains(t, out, "value: \"230.0\"")

	_, _, err = ctl(bg, "--server", server, "get", "node", "missing")
	assert.ErrorContains(t, err, "NotFound")

	// watch: initial state, then a twin change made in the API.
	var (
		wg       sync.WaitGroup
		watchOut string
		watchErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchOut, _, watchErr = ctl(bg, "--server", server, "watch", "devices/", "--max-events", "4")
	}()
	require.Eventually(t, func() bool { return p.store.Stats().Subscribers == 1 }, waitTimeout, 10*time.Millisecond)
	status, err := crd.DevicesV1beta1().DeviceStatuses("default").Get(bg, tireName, metav1.GetOptions{})
	require.NoError(t, err)
	status.Status.Twins[0].Reported.Value = "225.0"
	_, err = crd.DevicesV1beta1().DeviceStatuses("default").Update(bg, status, metav1.UpdateOptions{})
	require.NoError(t, err)
	wg.Wait()
	require.NoError(t, watchErr)

	events := parseWatchLines(t, watchOut)
	require.Len(t, events, 4)
	assert.Equal(t, []string{"RESET", "UPSERT", "SYNCED", "UPSERT"},
		[]string{events[0].Event, events[1].Event, events[2].Event, events[3].Event})
	assert.False(t, events[3].ReceivedAt.IsZero())
	assert.Equal(t, caps.Epoch+":4", events[3].ID)
	assert.Contains(t, string(events[3].Data), `"value":"225.0"`)

	_, metrics := httpGet(t, "http://"+p.HealthAddr().String()+"/metrics")
	for _, want := range []string{
		`dtpublisher_resources{kind="Device"} 1`,
		`dtpublisher_resources{kind="Node"} 2`,
		`dtpublisher_store_revision 4`,
		`dtpublisher_changes_total{type="upsert"} 4`,
		`dtpublisher_source_synced{source="devices"} 1`,
		`dtpublisher_source_synced{source="nodes"} 1`,
		`dtpublisher_subscribers 0`,
		`go_goroutines`,
	} {
		assert.Contains(t, metrics, want)
	}
}

// watchLine is one line of "ctl watch -o json".
type watchLine struct {
	ReceivedAt time.Time       `json:"receivedAt"`
	Event      string          `json:"event"`
	ID         string          `json:"id"`
	Data       json.RawMessage `json:"data"`
}

func parseWatchLines(t *testing.T, out string) []watchLine {
	t.Helper()
	var lines []watchLine
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var l watchLine
		require.NoError(t, json.Unmarshal([]byte(line), &l), line)
		lines = append(lines, l)
	}
	return lines
}

func TestQueryOffByDefault(t *testing.T) {
	kube, crd := fakeClients()
	cfg := insecureConfig()
	cfg.Query.Enabled = false
	p := run(t, cfg, Clients{Kube: kube, CRD: crd})

	_, _, err := ctl(context.Background(), "--server", "http://"+p.I4Addr().String(), "list", "nodes")
	assert.ErrorContains(t, err, "--enable-query")
}

func TestOnlyNodes(t *testing.T) {
	kube, crd := fakeClients()
	cfg := insecureConfig()
	no := false
	cfg.Sources.Devices.Enabled = &no
	p := run(t, cfg, Clients{Kube: kube, CRD: crd})

	out, _, err := ctl(context.Background(), "--server", "http://"+p.I4Addr().String(), "list", "devices")
	require.NoError(t, err)
	assert.Equal(t, "No resources.\n", out)
}

func TestServeMutualTLSWithCtl(t *testing.T) {
	ca := testpki.NewCA(t, "publisher-ca")
	server, consumerA := ca.Server("dtpublisher"), ca.Client("consumer-a")
	cfg := insecureConfig()
	cfg.Security = options.Security{TLS: security.ServerConfig{
		CertFile: server.CertFile, KeyFile: server.KeyFile, ClientCAFile: ca.CertFile, ClientAuth: security.ClientAuthRequire,
	}}
	cfg.Access.Consumers = []access.Consumer{{Name: "consumer-a", Prefixes: []string{"devices/"}}}
	kube, crd := fakeClients()
	p := run(t, cfg, Clients{Kube: kube, CRD: crd})
	url := "https://" + p.I4Addr().String()
	bg := context.Background()

	withCert := []string{"--server", url, "--ca-file", ca.CertFile, "--cert-file", consumerA.CertFile, "--key-file", consumerA.KeyFile}
	out, _, err := ctl(bg, append(withCert, "list", "devices")...)
	require.NoError(t, err)
	assert.Contains(t, out, tireName)

	_, _, err = ctl(bg, append(withCert, "list", "nodes")...)
	assert.ErrorContains(t, err, "Forbidden")

	_, _, err = ctl(bg, "--server", url, "--ca-file", ca.CertFile, "capabilities")
	assert.Error(t, err, "no client certificate")

	_, _, err = ctl(bg, "--server", url, "capabilities")
	assert.ErrorContains(t, err, "certificate", "the test CA is not a system root")
}

func TestNewPublisherFailures(t *testing.T) {
	kube, crd := fakeClients()
	clients := Clients{Kube: kube, CRD: crd}

	cfg := insecureConfig()
	cfg.Transport.Type = "zenoh"
	_, err := NewPublisher(cfg, clients)
	assert.ErrorIs(t, err, transport.ErrNotImplemented)
	assert.ErrorContains(t, err, "to be discussed with ZSC and DLR")

	cfg = options.NewConfig()
	cfg.Security.TLS = security.ServerConfig{CertFile: "/missing.crt", KeyFile: "/missing.key"}
	cfg.HealthAddress = "127.0.0.1:0"
	require.NoError(t, cfg.Validate())
	_, err = NewPublisher(cfg, clients)
	assert.ErrorContains(t, err, "load server certificate")

	// The health address is in use: the I-4 listener opened before it is closed again.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	i4Addr := free.Addr().String()
	require.NoError(t, free.Close())

	cfg = insecureConfig()
	cfg.Transport.HTTPSSE.Address = i4Addr
	cfg.HealthAddress = busy.Addr().String()
	_, err = NewPublisher(cfg, clients)
	assert.ErrorContains(t, err, "health endpoint")
	again, err := net.Listen("tcp", i4Addr)
	require.NoError(t, err, "the I-4 listener was closed")
	again.Close()
}

func TestVersionCommand(t *testing.T) {
	cmd := NewDTPublisherCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "dtpublisher v")
	assert.Contains(t, out.String(), "platform: ")
}
