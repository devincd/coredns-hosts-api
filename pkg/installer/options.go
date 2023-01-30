package installer

import "github.com/devincd/coredns-hosts-api/pkg/server"

type Args struct {
	// Kubeconfig  is absolute path to the kubeconfig file
	Kubeconfig                string
	CoreDNSName               string
	CoreDNSNamespace          string
	CoreDNSHostsServerVersion string
	ServerArgs                *server.Args
}

func NewEmptyArgs() *Args {
	return &Args{
		ServerArgs: &server.Args{},
	}
}
