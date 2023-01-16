package server

type Args struct {
	Addr     string
	FilePath string
	// Kubeconfig  is absolute path to the kubeconfig file
	Kubeconfig string
}
