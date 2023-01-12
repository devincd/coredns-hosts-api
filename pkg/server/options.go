package server

type Args struct {
	Addr     string `json:"addr"`
	FilePath string `json:"file_path"`
	// Kubeconfig  is absolute path to the kubeconfig file
	Kubeconfig string
}
