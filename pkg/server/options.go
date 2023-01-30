package server

type Args struct {
	Port int32
	// Kubeconfig  is absolute path to the kubeconfig file
	Kubeconfig string
}
