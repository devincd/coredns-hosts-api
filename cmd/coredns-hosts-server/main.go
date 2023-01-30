package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/devincd/coredns-hosts-api/pkg/server"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

var serverArgs server.Args

func main() {
	cmd := newCommand()
	if err := cmd.Execute(); err != nil {
		klog.ErrorS(err, "Failed to execute the command")
		os.Exit(-1)
	}
}

func newCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "coredns-hosts-server",
		Short: "coredns web apis service for hosts",
		Args:  cobra.ExactArgs(0),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			printFlags(cmd)
			stopCh := make(chan struct{})

			s, err := server.NewServer(serverArgs)
			if err != nil {
				return fmt.Errorf("failed to create server: %v", err)
			}
			if err := s.Run(stopCh); err != nil {
				return fmt.Errorf("failed to start server: %v", err)
			}
			WaitSignal(stopCh)
			return nil
		},
	}

	addFlags(command)
	return command
}

func addFlags(c *cobra.Command) {
	klog.InitFlags(flag.CommandLine)

	c.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	c.PersistentFlags().StringVar(&serverArgs.Kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	c.PersistentFlags().Int32Var(&serverArgs.Port, "port", 9080, "the web service port")
}

func printFlags(c *cobra.Command) {
	c.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		klog.Infof("FLAG: --%s=%q", flag.Name, flag.Value)
	})
}

func WaitSignal(stop chan struct{}) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	sigsInfo := <-sigs
	klog.Infof("Receive the signal %s, and the server is terminating", sigsInfo.String())
	close(stop)
}
