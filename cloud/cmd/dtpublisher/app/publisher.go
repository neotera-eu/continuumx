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
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	crdclientset "github.com/kubeedge/api/client/clientset/versioned"
	crdinformers "github.com/kubeedge/api/client/informers/externalversions"
	"github.com/neotera-eu/continuumx/cloud/cmd/dtpublisher/app/options"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/access"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source/device"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source/node"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/zenoh"
)

const healthShutdownTimeout = 5 * time.Second

// Clients are the API clients the publisher reads with.
type Clients struct {
	Kube kubernetes.Interface
	CRD  crdclientset.Interface
}

// Publisher is a running Device Twins Resources Publisher: sources, store, transport and the
// health and metrics endpoint.
type Publisher struct {
	store     *store.Store
	throttle  *source.Throttle
	sources   []source.Source
	transport transport.Publisher
	kubeInf   informers.SharedInformerFactory
	crdInf    crdinformers.SharedInformerFactory

	i4Listener     net.Listener
	healthListener net.Listener
	metrics        *prometheus.Registry
}

// NewPublisher wires a publisher from a validated configuration. It binds its listen
// addresses, so an address in use fails here, not later.
func NewPublisher(cfg *options.Config, clients Clients) (_ *Publisher, err error) {
	// p is not the named result: "return nil, err" must not clear it before the cleanup.
	p := &Publisher{store: store.New()}
	defer func() {
		if err != nil {
			p.closeListeners()
		}
	}()
	p.throttle = source.NewThrottle(p.store, cfg.MinPublishInterval.Duration, nil)

	info := transport.Info{MinPublishInterval: cfg.MinPublishInterval.Duration}
	if cfg.Sources.Nodes.IsEnabled() {
		p.kubeInf = informers.NewSharedInformerFactory(clients.Kube, 0)
		keys := cfg.Sources.Nodes.AnnotationKeys
		p.sources = append(p.sources, node.New(p.kubeInf.Core().V1().Nodes().Informer(), node.Config{AnnotationKeys: keys}))
		categories := []string{}
		for _, c := range model.NodeCategories() {
			categories = append(categories, string(c))
		}
		info.Kinds = append(info.Kinds, transport.KindInfo{
			Kind: model.KindNode, Prefix: model.NodesPrefix + model.KeySeparator,
			Categories: categories, AnnotationKeys: keys,
		})
	}
	if cfg.Sources.Devices.IsEnabled() {
		p.crdInf = crdinformers.NewSharedInformerFactory(clients.CRD, 0)
		d := p.crdInf.Devices().V1beta1()
		keys := cfg.Sources.Devices.AnnotationKeys
		p.sources = append(p.sources, device.New(device.Informers{
			Devices:        d.Devices().Informer(),
			DeviceModels:   d.DeviceModels().Informer(),
			DeviceStatuses: d.DeviceStatuses().Informer(),
		}, device.Config{AnnotationKeys: keys}))
		info.Kinds = append(info.Kinds, transport.KindInfo{
			Kind: model.KindDevice, Prefix: model.DevicesPrefix + model.KeySeparator, AnnotationKeys: keys,
		})
	}

	registry := transport.NewRegistry()
	if err := registry.Register(zenoh.Name, zenoh.Factory(zenoh.Options{})); err != nil {
		return nil, err
	}
	if cfg.Transport.Type == httpsse.Name {
		opts, err := p.httpsseOptions(cfg)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(httpsse.Name, httpsse.Factory(opts)); err != nil {
			return nil, err
		}
	}
	if p.transport, err = registry.New(cfg.Transport.Type, transport.Deps{Store: p.store, Info: info}); err != nil {
		return nil, err
	}

	if p.healthListener, err = net.Listen("tcp", cfg.HealthAddress); err != nil {
		return nil, fmt.Errorf("health endpoint: %w", err)
	}
	p.metrics = prometheus.NewRegistry()
	p.metrics.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		&storeCollector{store: p.store, sources: p.sources},
	)
	return p, nil
}

// httpsseOptions builds the transport options, including TLS, access and the listener.
func (p *Publisher) httpsseOptions(cfg *options.Config) (httpsse.Options, error) {
	h := cfg.Transport.HTTPSSE
	opts := httpsse.Options{
		HeartbeatInterval: h.HeartbeatInterval.Duration,
		BufferSize:        h.BufferSize,
		WriteTimeout:      h.WriteTimeout.Duration,
		QueryEnabled:      cfg.Query.Enabled,
		Insecure:          cfg.Security.InsecureDev,
	}
	if !cfg.Security.InsecureDev {
		tlsCfg, err := cfg.Security.TLS.TLSConfig()
		if err != nil {
			return opts, err
		}
		opts.TLSConfig = tlsCfg
	}
	if len(cfg.Access.Consumers) > 0 {
		authz, err := access.NewStatic(cfg.Access.Consumers)
		if err != nil {
			return opts, err
		}
		opts.Authorizer = authz
	}
	ln, err := net.Listen("tcp", h.Address)
	if err != nil {
		return opts, fmt.Errorf("transport %s: %w", httpsse.Name, err)
	}
	p.i4Listener = ln
	opts.Listener = ln
	return opts, nil
}

func (p *Publisher) closeListeners() {
	for _, ln := range []net.Listener{p.i4Listener, p.healthListener} {
		if ln != nil {
			_ = ln.Close()
		}
	}
}

// I4Addr is the address the I-4 transport listens on, nil if it does not listen on TCP here.
func (p *Publisher) I4Addr() net.Addr {
	if p.i4Listener == nil {
		return nil
	}
	return p.i4Listener.Addr()
}

// HealthAddr is the address of the health and metrics endpoint.
func (p *Publisher) HealthAddr() net.Addr {
	return p.healthListener.Addr()
}

// Ready reports whether every source has published its initial state.
func (p *Publisher) Ready() bool {
	return len(p.notReady()) == 0
}

func (p *Publisher) notReady() []string {
	var names []string
	for _, s := range p.sources {
		if !s.Synced() {
			names = append(names, s.Name())
		}
	}
	return names
}

// Run publishes until ctx is done or a part fails. Cancelling ctx is a normal stop and
// returns nil.
func (p *Publisher) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	start := func(name string, run func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				mu.Unlock()
				cancel()
			}
		}()
	}

	for _, s := range p.sources {
		s := s
		start("source "+s.Name(), func(ctx context.Context) error { return s.Run(ctx, p.throttle) })
	}
	if p.kubeInf != nil {
		p.kubeInf.Start(ctx.Done())
	}
	if p.crdInf != nil {
		p.crdInf.Start(ctx.Done())
	}
	start("transport "+p.transport.Name(), p.transport.Run)
	start("health endpoint", p.serveHealth)
	klog.Infof("dtpublisher: running; sources %v, transport %s", p.sourceNames(), p.transport.Name())

	<-ctx.Done()
	wg.Wait()
	p.throttle.Stop()
	if p.kubeInf != nil {
		p.kubeInf.Shutdown()
	}
	if p.crdInf != nil {
		p.crdInf.Shutdown()
	}
	return errors.Join(errs...)
}

func (p *Publisher) sourceNames() []string {
	names := make([]string, 0, len(p.sources))
	for _, s := range p.sources {
		names = append(names, s.Name())
	}
	return names
}

// serveHealth serves /healthz (the process runs), /readyz (every source has published its
// initial state) and /metrics on the health listener until ctx is done.
func (p *Publisher) serveHealth(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if waiting := p.notReady(); len(waiting) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "waiting for sources: %s\n", strings.Join(waiting, ", "))
			return
		}
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(p.metrics, promhttp.HandlerOpts{}))

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(p.healthListener) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), healthShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// storeCollector exports the store counters and source state as Prometheus metrics.
type storeCollector struct {
	store   *store.Store
	sources []source.Source
}

var (
	descRevision = prometheus.NewDesc("dtpublisher_store_revision",
		"Revision of the last published change.", nil, nil)
	descResources = prometheus.NewDesc("dtpublisher_resources",
		"Published resources per kind.", []string{"kind"}, nil)
	descSubscribers = prometheus.NewDesc("dtpublisher_subscribers",
		"Open subscriptions (watch streams).", nil, nil)
	descChanges = prometheus.NewDesc("dtpublisher_changes_total",
		"Published changes per type.", []string{"type"}, nil)
	descOverflows = prometheus.NewDesc("dtpublisher_subscription_overflows_total",
		"Subscriptions closed because the subscriber fell behind.", nil, nil)
	descSynced = prometheus.NewDesc("dtpublisher_source_synced",
		"1 once a source has published its initial state.", []string{"source"}, nil)
)

func (c *storeCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descRevision, descResources, descSubscribers, descChanges, descOverflows, descSynced} {
		ch <- d
	}
}

func (c *storeCollector) Collect(ch chan<- prometheus.Metric) {
	st := c.store.Stats()
	ch <- prometheus.MustNewConstMetric(descRevision, prometheus.GaugeValue, float64(st.Revision))
	kinds := []string{string(model.KindNode), string(model.KindDevice)}
	for k := range st.Resources {
		if k != model.KindNode && k != model.KindDevice {
			kinds = append(kinds, string(k))
		}
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		ch <- prometheus.MustNewConstMetric(descResources, prometheus.GaugeValue, float64(st.Resources[model.Kind(k)]), k)
	}
	ch <- prometheus.MustNewConstMetric(descSubscribers, prometheus.GaugeValue, float64(st.Subscribers))
	ch <- prometheus.MustNewConstMetric(descChanges, prometheus.CounterValue, float64(st.Upserts), "upsert")
	ch <- prometheus.MustNewConstMetric(descChanges, prometheus.CounterValue, float64(st.Deletes), "delete")
	ch <- prometheus.MustNewConstMetric(descOverflows, prometheus.CounterValue, float64(st.Overflows))
	for _, s := range c.sources {
		v := 0.0
		if s.Synced() {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(descSynced, prometheus.GaugeValue, v, s.Name())
	}
}
