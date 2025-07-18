// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cilium/cilium/cilium-cli/api"
	"github.com/cilium/cilium/cilium-cli/k8s"
	"github.com/cilium/cilium/pkg/cmdref"
)

var (
	contextName       string
	namespace         string
	impersonateAs     string
	impersonateGroups []string
	helmReleaseName   string
	kubeConfig        string

	k8sClient *k8s.Client
)

// NewDefaultCiliumCommand returns a new "cilium" cli cobra command without any additional hooks.
func NewDefaultCiliumCommand() *cobra.Command {
	return NewCiliumCommand(&api.NopHooks{})
}

// NewCiliumCommand returns a new "cilium" cli cobra command registering all the additional input hooks.
func NewCiliumCommand(hooks api.Hooks) *cobra.Command {
	cmd := &cobra.Command{
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// return early for commands that don't require the kubernetes client
			if !cmd.HasParent() { // this is root
				return nil
			}
			switch cmd.Name() {
			case "completion", "help", "summary":
				return nil
			case "version":
				if clientFlag, err := cmd.Flags().GetBool("client"); err == nil && clientFlag {
					return nil
				}
			}

			c, err := k8s.NewClient(contextName, kubeConfig, namespace, impersonateAs, impersonateGroups)
			if err != nil {
				return fmt.Errorf("unable to create Kubernetes client: %w", err)
			}

			k8sClient = c
			ctx := api.SetNamespaceContextValue(context.Background(), namespace)
			ctx = api.SetK8sClientContextValue(ctx, k8sClient)
			cmd.SetContext(ctx)
			return nil
		},
		Run: func(cmd *cobra.Command, _ []string) {
			cmd.Help()
		},
		Use:   "cilium",
		Short: "Cilium provides eBPF-based Networking, Security, and Observability for Kubernetes",
		Long: `CLI to install, manage, & troubleshooting Cilium clusters running Kubernetes.

Cilium is a CNI for Kubernetes to provide secure network connectivity and
load-balancing with excellent visibility using eBPF

Examples:

Install Cilium in current Kubernetes context

  $ cilium install

Check status of Cilium

  $ cilium status

Enable the Hubble observability layer

  $ cilium hubble enable

Perform a connectivity test

  $ cilium connectivity test`,
		SilenceErrors: true, // this is being handled in main, no need to duplicate error messages
		SilenceUsage:  true, // avoid showing help when usage is correct but an error occurred
	}

	cmd.PersistentFlags().StringVar(&contextName, "context", "", "Kubernetes configuration context")
	cmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "kube-system", "Namespace Cilium is running in")
	cmd.PersistentFlags().StringVar(&impersonateAs, "as", "", "Username to impersonate for the operation. User could be a regular user or a service account in a namespace.")
	cmd.PersistentFlags().StringArrayVar(&impersonateGroups, "as-group", []string{}, "Group to impersonate for the operation, this flag can be repeated to specify multiple groups.")
	cmd.PersistentFlags().StringVar(&helmReleaseName, "helm-release-name", "cilium", "Helm release name")
	cmd.PersistentFlags().StringVar(&kubeConfig, "kubeconfig", "", "Path to the kubeconfig file")

	cmd.AddCommand(
		newCmdBgp(),
		newCmdClusterMesh(),
		newCmdConfig(),
		newCmdConnectivity(hooks),
		newCmdContext(),
		newCmdEncrypt(),
		newCmdHubble(),
		newCmdMulticast(),
		newCmdStatus(),
		newCmdSysdump(hooks),
		newCmdVersion(),
		newCmdInstallWithHelm(),
		newCmdUninstallWithHelm(),
		newCmdUpgradeWithHelm(),
		newCmdFeatures(),
		cmdref.NewCmd(cmd),
	)

	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	hooks.InitializeCommand(cmd)
	return cmd
}
