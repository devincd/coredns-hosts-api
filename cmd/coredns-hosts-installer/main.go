package main

import (
	"flag"
	"fmt"
	"github.com/devincd/coredns-hosts-api/pkg/installer"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
	"os"
)

var installerArgs = installer.NewEmptyArgs()

func main() {
	cmd := newCommand()
	if err := cmd.Execute(); err != nil {
		klog.ErrorS(err, "Failed to execute the command")
		os.Exit(-1)
	}
}

func newCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "coredns-hosts-install",
		Short: "coredns web apis service for hosts",
		Args:  cobra.ExactArgs(0),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			printFlags(cmd)
			s, err := installer.NewServer(installerArgs)
			if err != nil {
				return fmt.Errorf("failed to create server: %v", err)
			}
			if err := s.RunOnce(); err != nil {
				return fmt.Errorf("failed to RunOnce server: %v", err)
			}
			return nil
		},
	}
	addFlags(command)
	return command
}

func addFlags(c *cobra.Command) {
	klog.InitFlags(flag.CommandLine)

	c.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	c.PersistentFlags().StringVar(&installerArgs.Kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	c.PersistentFlags().StringVar(&installerArgs.CoreDNSName, "coredns-name", "coredns", "the name of coreDNS component, including the Deployment and Service.")
	c.PersistentFlags().StringVar(&installerArgs.CoreDNSNamespace, "coredns-namespace", "kube-system", "the namespace of coreDNS component, including the Deployment and Service.")
	c.PersistentFlags().StringVar(&installerArgs.CoreDNSHostsServerVersion, "corednsHostsServer-version", "v0.0.1", "")
	c.PersistentFlags().StringVar(&installerArgs.ServerArgs.Kubeconfig, "server-kubeconfig", "", "absolute path to the kubeconfig file of coredns-hosts-server component")
	c.PersistentFlags().Int32Var(&installerArgs.ServerArgs.Port, "server-port", 9080, "the web service port of coredns-hosts-server component")
}

func printFlags(c *cobra.Command) {
	c.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		klog.Infof("FLAG: --%s=%q", flag.Name, flag.Value)
	})
}
