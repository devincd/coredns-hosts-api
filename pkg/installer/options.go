package installer

import "github.com/devincd/coredns-hosts-api/pkg/server"

type Args struct {
	// Kubeconfig  is absolute path to the kubeconfig file
	Kubeconfig string

	ServerArgs *server.Args
}
