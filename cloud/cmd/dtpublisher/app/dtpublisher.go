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

// Package app is the dtpublisher command: "serve" runs the Device Twins Resources Publisher
// (UP2DATE4SDV CID 93), "ctl" is its operator client.
package app

import (
	goflag "flag"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	crdclientset "github.com/kubeedge/api/client/clientset/versioned"
	"github.com/neotera-eu/continuumx/cloud/cmd/dtpublisher/app/options"
	"github.com/neotera-eu/continuumx/pkg/version"
)

// NewDTPublisherCommand returns the root command.
func NewDTPublisherCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "dtpublisher",
		Short: "Device Twins Resources Publisher (CID 93)",
		Long: `dtpublisher publishes the resource context of the vehicle-to-cloud continuum, Kubernetes
nodes and device twins, to authorised consumers over Interface I-4. It only reads and
publishes; it never selects targets, schedules, rolls out or offloads workloads.

  serve   run the publisher
  ctl     query and watch a running publisher`,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	klogFlags := goflag.NewFlagSet("klog", goflag.ContinueOnError)
	klog.InitFlags(klogFlags)
	root.PersistentFlags().AddGoFlagSet(klogFlags)

	root.AddCommand(newServeCommand(), newCtlCommand(), newVersionCommand())
	return root
}

func newServeCommand() *cobra.Command {
	flags := &options.Flags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the publisher",
		Long: `Run the Device Twins Resources Publisher. It reads Nodes and Device, DeviceModel and
DeviceStatus objects from the Kubernetes API and publishes them over the configured I-4
transport. By default it publishes continuously to subscribers (watch); list and get are
opt-in (--enable-query). TLS is required unless --insecure-dev is set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.Config()
			if err != nil {
				return err
			}
			klog.Infof("dtpublisher version: %#v", version.Get())
			if cfg.Security.InsecureDev {
				klog.Warning("dtpublisher: serving plain HTTP without TLS (--insecure-dev); for development playgrounds only")
			}

			restCfg, err := clientcmd.BuildConfigFromFlags(cfg.Master, cfg.Kubeconfig)
			if err != nil {
				return fmt.Errorf("kubernetes client config: %w", err)
			}
			kube, err := kubernetes.NewForConfig(restCfg)
			if err != nil {
				return err
			}
			crd, err := crdclientset.NewForConfig(restCfg)
			if err != nil {
				return err
			}
			p, err := NewPublisher(cfg, Clients{Kube: kube, CRD: crd})
			if err != nil {
				return err
			}
			if addr := p.I4Addr(); addr != nil {
				klog.Infof("dtpublisher: I-4 on %s, health and metrics on %s", addr, p.HealthAddr())
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return p.Run(ctx)
		},
	}
	flags.AddFlags(cmd.Flags())
	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			v := version.Get()
			fmt.Fprintf(cmd.OutOrStdout(), "dtpublisher %s\n  commit:   %s (%s)\n  built:    %s\n  go:       %s\n  platform: %s\n",
				v.GitVersion, v.GitCommit, v.GitTreeState, v.BuildDate, v.GoVersion, v.Platform)
		},
	}
}
