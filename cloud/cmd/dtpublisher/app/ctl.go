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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/client"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/security"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

const (
	// EnvServer is the environment variable for the default --server.
	EnvServer     = "DTPUBLISHER_SERVER"
	defaultServer = "https://localhost:8443"

	outputTable = "table"
	outputJSON  = "json"
	outputYAML  = "yaml"
	outputText  = "text"
)

// Reconnect delays of ctl watch, doubled after each failed attempt. Variables for tests.
var (
	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// ctlOptions are the connection and output options shared by the ctl subcommands.
type ctlOptions struct {
	server     string
	caFile     string
	certFile   string
	keyFile    string
	serverName string
	output     string
}

func (o *ctlOptions) client() (*client.Client, error) {
	var opts []client.Option
	if strings.HasPrefix(o.server, "https://") {
		tlsCfg, err := security.ClientConfig{CAFile: o.caFile, CertFile: o.certFile, KeyFile: o.keyFile, ServerName: o.serverName}.TLSConfig()
		if err != nil {
			return nil, err
		}
		opts = append(opts, client.WithTLS(tlsCfg))
	}
	return client.New(o.server, opts...)
}

// format returns the output format, or def if none was given, and checks it is one of valid.
func (o *ctlOptions) format(def string, valid ...string) (string, error) {
	if o.output == "" {
		return def, nil
	}
	for _, v := range valid {
		if o.output == v {
			return v, nil
		}
	}
	return "", fmt.Errorf("output %q is not supported here, valid: %s", o.output, strings.Join(valid, ", "))
}

func newCtlCommand() *cobra.Command {
	o := &ctlOptions{}
	cmd := &cobra.Command{
		Use:   "ctl",
		Short: "Query and watch a running publisher",
		Long: `ctl is the operator client of a running publisher (httpsse transport).

  capabilities                         what is published and which operations are served
  watch [PREFIX]                       subscribe to the standing data flow (default operation)
  list nodes|devices                   list resources (needs --enable-query on the publisher)
  get node NAME | device NS/NAME       get one resource (needs --enable-query on the publisher)

The server is --server, or $` + EnvServer + `, or ` + defaultServer + `.`,
		Args: cobra.NoArgs,
	}
	server := os.Getenv(EnvServer)
	if server == "" {
		server = defaultServer
	}
	fs := cmd.PersistentFlags()
	fs.StringVar(&o.server, "server", server, "Publisher URL, https://host:port (or http:// for an --insecure-dev publisher).")
	fs.StringVar(&o.caFile, "ca-file", "", "CA bundle the publisher certificate is verified against. Default: system roots.")
	fs.StringVar(&o.certFile, "cert-file", "", "Client certificate, for mutual TLS.")
	fs.StringVar(&o.keyFile, "key-file", "", "Client key, for mutual TLS.")
	fs.StringVar(&o.serverName, "server-name", "", "Name the publisher certificate is verified for, if not the --server host.")
	fs.StringVarP(&o.output, "output", "o", "", "Output format: table, json or yaml (watch: json or text).")

	cmd.AddCommand(newCapabilitiesCommand(o), newListCommand(o), newGetCommand(o), newWatchCommand(o))
	return cmd
}

func newCapabilitiesCommand(o *ctlOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "capabilities",
		Short: "Show what the publisher publishes and which operations it serves",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := o.format(outputYAML, outputYAML, outputJSON)
			if err != nil {
				return err
			}
			c, err := o.client()
			if err != nil {
				return err
			}
			caps, err := c.Capabilities(cmd.Context())
			if err != nil {
				return explain(err)
			}
			return printObject(cmd.OutOrStdout(), format, caps)
		},
	}
}

func newListCommand(o *ctlOptions) *cobra.Command {
	var category, namespace, nodeName string
	nodes := &cobra.Command{
		Use:   "nodes",
		Short: "List nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd, o, func(ctx context.Context, c *client.Client) (*client.List, error) {
				return c.ListNodes(ctx, client.ListNodesOptions{Category: model.NodeCategory(category)})
			})
		},
	}
	nodes.Flags().StringVar(&category, "category", "", "Only this category: cloud-control-plane, cloud-worker or edge-worker.")

	devices := &cobra.Command{
		Use:   "devices",
		Short: "List devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd, o, func(ctx context.Context, c *client.Client) (*client.List, error) {
				return c.ListDevices(ctx, client.ListDevicesOptions{Namespace: namespace, Node: nodeName})
			})
		},
	}
	devices.Flags().StringVar(&namespace, "namespace", "", "Only this namespace.")
	devices.Flags().StringVar(&nodeName, "node", "", "Only devices hosted by this node.")

	cmd := &cobra.Command{Use: "list", Short: "List nodes or devices (opt-in on the publisher)", Args: cobra.NoArgs}
	cmd.AddCommand(nodes, devices)
	return cmd
}

func runList(cmd *cobra.Command, o *ctlOptions, list func(context.Context, *client.Client) (*client.List, error)) error {
	format, err := o.format(outputTable, outputTable, outputJSON, outputYAML)
	if err != nil {
		return err
	}
	c, err := o.client()
	if err != nil {
		return err
	}
	l, err := list(cmd.Context(), c)
	if err != nil {
		return explain(err)
	}
	if format != outputTable {
		return printObject(cmd.OutOrStdout(), format, struct {
			httpsse.Cursor
			Items []model.Resource `json:"items"`
		}{l.Cursor, l.Items})
	}
	return printTable(cmd.OutOrStdout(), l.Items)
}

func newGetCommand(o *ctlOptions) *cobra.Command {
	var properties []string
	nodeCmd := &cobra.Command{
		Use:   "node NAME",
		Short: "Get one node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(cmd, o, func(ctx context.Context, c *client.Client) (*model.Resource, error) {
				return c.GetNode(ctx, args[0])
			})
		},
	}
	deviceCmd := &cobra.Command{
		Use:   "device NAMESPACE/NAME",
		Short: "Get one device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace, name, ok := strings.Cut(args[0], "/")
			if !ok || namespace == "" || name == "" || strings.Contains(name, "/") {
				return fmt.Errorf("device %q: want NAMESPACE/NAME", args[0])
			}
			return runGet(cmd, o, func(ctx context.Context, c *client.Client) (*model.Resource, error) {
				return c.GetDevice(ctx, namespace, name, properties...)
			})
		},
	}
	deviceCmd.Flags().StringArrayVar(&properties, "property", nil, "Only this property; repeat for more.")

	cmd := &cobra.Command{Use: "get", Short: "Get one node or device (opt-in on the publisher)", Args: cobra.NoArgs}
	cmd.AddCommand(nodeCmd, deviceCmd)
	return cmd
}

func runGet(cmd *cobra.Command, o *ctlOptions, get func(context.Context, *client.Client) (*model.Resource, error)) error {
	format, err := o.format(outputYAML, outputYAML, outputJSON)
	if err != nil {
		return err
	}
	c, err := o.client()
	if err != nil {
		return err
	}
	r, err := get(cmd.Context(), c)
	if err != nil {
		return explain(err)
	}
	return printObject(cmd.OutOrStdout(), format, r)
}

func newWatchCommand(o *ctlOptions) *cobra.Command {
	var (
		since       uint64
		epoch       string
		noReconnect bool
		maxEvents   int
	)
	cmd := &cobra.Command{
		Use:   "watch [PREFIX]",
		Short: "Subscribe to the standing data flow",
		Long: `Subscribe to the publisher and print every event: first the current state, then each change.
PREFIX selects keys, for example "nodes/", "nodes/edge-worker/" or "devices/default/".

Output (-o json, the default) is one JSON object per line with the local receive time:
  {"receivedAt":"...","event":"UPSERT","id":"<epoch>:<revision>","data":{...}}
-o text prints one short line per event.

When the stream ends, watch reconnects from the last event ID, so no change is missed, unless
--no-reconnect is set. A refused subscription (4xx) is not retried.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := o.format(outputJSON, outputJSON, outputText)
			if err != nil {
				return err
			}
			c, err := o.client()
			if err != nil {
				return err
			}
			w := &watchRun{
				client: c, out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(), format: format,
				noReconnect: noReconnect, maxEvents: maxEvents,
			}
			if len(args) == 1 {
				w.prefix = args[0]
			}
			if cmd.Flags().Changed("since") {
				if epoch == "" {
					return errors.New("--since needs --epoch")
				}
				w.cursor = &httpsse.Cursor{Epoch: epoch, Revision: since}
			}
			return w.run(cmd.Context())
		},
	}
	cmd.Flags().Uint64Var(&since, "since", 0, "Resume from this revision (with --epoch), for example from a list or an earlier watch.")
	cmd.Flags().StringVar(&epoch, "epoch", "", "Epoch of --since.")
	cmd.Flags().BoolVar(&noReconnect, "no-reconnect", false, "Exit when the stream ends instead of reconnecting.")
	cmd.Flags().IntVar(&maxEvents, "max-events", 0, "Exit after this many events. 0: no limit.")
	return cmd
}

// watchRun is one ctl watch, across reconnects.
type watchRun struct {
	client      *client.Client
	out, errOut io.Writer
	format      string
	prefix      string
	cursor      *httpsse.Cursor
	noReconnect bool
	maxEvents   int
	printed     int
}

var errMaxEvents = errors.New("max events reached")

func (w *watchRun) run(ctx context.Context) error {
	backoff := reconnectMin
	for {
		synced, err := w.once(ctx)
		switch {
		case errors.Is(err, errMaxEvents) || ctx.Err() != nil:
			return nil
		case err == nil:
			err = client.ErrStreamEnded
		}
		switch {
		case w.noReconnect || isPermanent(err):
			return explain(err)
		}
		if synced {
			backoff = reconnectMin
		}
		from := "a fresh start"
		if w.cursor != nil {
			from = httpsse.EventID(*w.cursor)
		}
		fmt.Fprintf(w.errOut, "watch: %v; reconnecting in %s from %s\n", err, backoff, from)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

// once runs one stream until it ends. It reports whether the stream reached SYNCED and why
// it ended.
func (w *watchRun) once(ctx context.Context) (bool, error) {
	watcher, err := w.client.Watch(ctx, client.WatchOptions{Prefix: w.prefix, Since: w.cursor})
	if err != nil {
		return false, err
	}
	defer watcher.Close()
	synced := false
	for ev := range watcher.Events() {
		if ev.Type == httpsse.EventSynced {
			synced = true
		}
		if err := w.print(ev); err != nil {
			return synced, err
		}
		w.printed++
		if w.maxEvents > 0 && w.printed >= w.maxEvents {
			return synced, errMaxEvents
		}
	}
	if c, ok := watcher.Cursor(); ok {
		w.cursor = &c
	}
	if err := watcher.Err(); err != nil {
		return synced, err
	}
	return synced, ctx.Err()
}

func (w *watchRun) print(ev client.Event) error {
	now := time.Now().UTC()
	if w.format == outputText {
		_, err := fmt.Fprintf(w.out, "%s %-9s %s\n", now.Format(time.RFC3339Nano), ev.Type, describe(ev))
		return err
	}
	line, err := json.Marshal(struct {
		ReceivedAt time.Time       `json:"receivedAt"`
		Event      string          `json:"event"`
		ID         string          `json:"id,omitempty"`
		Data       json.RawMessage `json:"data"`
	}{now, ev.Type, ev.ID, ev.Data})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w.out, "%s\n", line)
	return err
}

func describe(ev client.Event) string {
	switch {
	case ev.Resource != nil:
		return fmt.Sprintf("%s rev=%d", ev.Resource.Key, ev.Resource.Revision)
	case ev.Deletion != nil:
		return fmt.Sprintf("%s rev=%d", ev.Deletion.Key, ev.Deletion.Revision)
	case ev.Cursor != nil:
		return httpsse.EventID(*ev.Cursor)
	case ev.Error != nil:
		return fmt.Sprintf("%s: %s (resume %s)", ev.Error.Reason, ev.Error.Message, httpsse.EventID(ev.Error.Cursor))
	}
	return string(ev.Data)
}

// isPermanent reports whether retrying cannot help: the publisher refused the request.
func isPermanent(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// explain adds a hint to errors an operator can act on.
func explain(err error) error {
	switch {
	case errors.Is(err, client.ErrQueryDisabled):
		return fmt.Errorf("%w (start the publisher with --enable-query, or use ctl watch)", err)
	case errors.Is(err, client.ErrUnauthenticated):
		return fmt.Errorf("%w (give a client certificate with --cert-file and --key-file)", err)
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && apiErr.Reason == "" {
		return fmt.Errorf("%w (is --server a dtpublisher?)", err)
	}
	return err
}

func printObject(out io.Writer, format string, v interface{}) error {
	var (
		data []byte
		err  error
	)
	if format == outputJSON {
		data, err = json.MarshalIndent(v, "", "  ")
		data = append(data, '\n')
	} else {
		data, err = yaml.Marshal(v)
	}
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// printTable prints resources as a table, one layout per kind, nodes first.
func printTable(out io.Writer, items []model.Resource) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	var nodes, devices, other []model.Resource
	for _, r := range items {
		switch r.Spec.(type) {
		case *model.NodeSpec:
			nodes = append(nodes, r)
		case *model.DeviceSpec:
			devices = append(devices, r)
		default:
			other = append(other, r)
		}
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(out, "No resources.")
		return err
	}
	if len(nodes) > 0 {
		fmt.Fprintln(tw, "NAME\tCATEGORY\tREADY\tCPU\tMEMORY\tREVISION")
		for _, r := range nodes {
			s := r.Spec.(*model.NodeSpec)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", s.Name, s.Category, s.Ready,
				orDash(s.Allocatable["cpu"]), orDash(s.Allocatable["memory"]), r.Revision)
		}
	}
	if len(devices) > 0 {
		if len(nodes) > 0 {
			fmt.Fprintln(tw)
		}
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tNODE\tSTATE\tPROPERTIES\tREVISION")
		for _, r := range devices {
			s := r.Spec.(*model.DeviceSpec)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\n", s.Namespace, s.Name, orDash(s.Node), orDash(s.State), len(s.Properties), r.Revision)
		}
	}
	if len(other) > 0 {
		fmt.Fprintln(tw, "\nKEY\tKIND\tREVISION")
		for _, r := range other {
			fmt.Fprintf(tw, "%s\t%s\t%d\n", r.Key, r.Kind, r.Revision)
		}
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
