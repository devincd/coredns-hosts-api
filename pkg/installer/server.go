package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/coredns/caddy/caddyfile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

type Server struct {
	clientset         *kubernetes.Clientset
	corednsDeployment *appsv1.Deployment
	args              *Args
}

func NewServer(args *Args) (*Server, error) {
	s := &Server{
		args: args,
	}
	if err := s.initKubeClient(args); err != nil {
		return nil, fmt.Errorf("failed to initKubeClient: %v", err)
	}
	if err := s.initCorednsDeployment(args); err != nil {
		return nil, fmt.Errorf("failed to initCorednsDeployment: %v", err)
	}
	return s, nil
}

// initKubeClient creates the k8s client if running in a k8s environment.
func (s *Server) initKubeClient(args *Args) error {
	kconfig := args.Kubeconfig
	if kconfig == "" {
		home := homedir.HomeDir()
		if home != "" && FileExist(filepath.Join(home, ".kube", "config")) {
			kconfig = filepath.Join(home, ".kube", "config")
		}
	}
	kubeconfig, err := clientcmd.BuildConfigFromFlags("", kconfig)
	if err != nil {
		return err
	}
	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		return err
	}
	s.clientset = clientset
	return nil
}

func (s *Server) initCorednsDeployment(args *Args) error {
	if s.clientset == nil {
		return fmt.Errorf("the k8s clientset can not be nil")
	}
	deploy, err := s.clientset.AppsV1().Deployments(args.CoreDNSNamespace).Get(context.TODO(), args.CoreDNSName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	s.corednsDeployment = deploy
	return nil
}

func FileExist(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

func (s *Server) RunOnce() error {
	if err := s.ensureClusterrole(); err != nil {
		return fmt.Errorf("failed to ensureClusterrole:%v", err)
	}
	if err := s.ensureDeployment(); err != nil {
		return fmt.Errorf("failed to ensureDeployment:%v", err)
	}
	if err := s.ensureService(); err != nil {
		return fmt.Errorf("failed to ensureService:%v", err)
	}
	if err := s.ensureCoreDNSConfigmap(); err != nil {
		return fmt.Errorf("failed to ensureCoreDNSConfigmap:%v", err)
	}
	return nil
}

func (s *Server) ensureClusterrole() error {
	if s.corednsDeployment == nil {
		return fmt.Errorf("the coredns deployment can not be nil")
	}
	// search ServiceAccount from Deployment
	var serviceAccountName string
	serviceAccountName = s.corednsDeployment.Spec.Template.Spec.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = s.corednsDeployment.Spec.Template.Spec.DeprecatedServiceAccount
	}
	if serviceAccountName == "" {
		return fmt.Errorf("the serviceAccountName can not be empty")
	}
	serviceAccountNamespace := s.corednsDeployment.Namespace
	clusterRoleBindingList, err := s.clientset.RbacV1().ClusterRoleBindings().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	var clusterRoleName string
	for _, item := range clusterRoleBindingList.Items {
		for _, subject := range item.Subjects {
			if subject.Name == serviceAccountName && subject.Kind == "ServiceAccount" && subject.Namespace == serviceAccountNamespace {
				if item.RoleRef.Kind == "ClusterRole" {
					clusterRoleName = item.RoleRef.Name
				}
			}
		}
	}
	if clusterRoleName == "" {
		return fmt.Errorf("the clusterRoleName can not be empty")
	}
	// update
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result, getErr := s.clientset.RbacV1().ClusterRoles().Get(context.TODO(), clusterRoleName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("failed to get latest version of Cluster: %v", getErr)
		}
		addRule := rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"*"},
		}
		if !ExistPolicyRule(addRule, result.Rules) {
			result.Rules = append(result.Rules, addRule)
			_, updateErr := s.clientset.RbacV1().ClusterRoles().Update(context.TODO(), result, metav1.UpdateOptions{})
			return updateErr
		}
		return nil
	})
	return retryErr
}

func (s *Server) ensureDeployment() error {
	volumeName := "shared-data"
	volumeMountItem := corev1.VolumeMount{
		Name:      volumeName,
		MountPath: "/etc/coredns-dir",
	}
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of Deployment before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		result, getErr := s.clientset.AppsV1().Deployments(s.corednsDeployment.Namespace).Get(context.TODO(), s.corednsDeployment.Name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("failed to get latest version of Deployment: %v", getErr)
		}
		var needUpdate bool
		// add Container
		coreDNSHostsServerName := "coredns-hosts-server"
		if !ExistContainerByName(coreDNSHostsServerName, result.Spec.Template.Spec.Containers) {
			needUpdate = true
			result.Spec.Template.Spec.Containers = append(result.Spec.Template.Spec.Containers, corev1.Container{
				Name:            coreDNSHostsServerName,
				Image:           fmt.Sprintf("docker.io/devincd/coredns-hosts-server:%s", s.args.CoreDNSHostsServerVersion),
				ImagePullPolicy: corev1.PullAlways,
				Args: []string{
					"--kubeconfig", s.args.ServerArgs.Kubeconfig,
					"--port", fmt.Sprintf("%d", s.args.ServerArgs.Port),
				},
				Ports: []corev1.ContainerPort{
					{
						ContainerPort: s.args.ServerArgs.Port,
					},
				},
			})
		}
		// add container volumeMount
		for index, container := range result.Spec.Template.Spec.Containers {
			if !ExistVolumeMountsByName(volumeName, container.VolumeMounts) {
				needUpdate = true
				result.Spec.Template.Spec.Containers[index].VolumeMounts = append(result.Spec.Template.Spec.Containers[index].VolumeMounts, volumeMountItem)
			}
		}
		// add volume
		if !ExistVolumeMsByName(volumeName, result.Spec.Template.Spec.Volumes) {
			needUpdate = true
			result.Spec.Template.Spec.Volumes = append(result.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})
		}
		if needUpdate {
			_, updateErr := s.clientset.AppsV1().Deployments(s.corednsDeployment.Namespace).Update(context.TODO(), result, metav1.UpdateOptions{})
			return updateErr
		}
		return nil
	})
	return retryErr
}

func ExistPolicyRule(rule rbacv1.PolicyRule, rules []rbacv1.PolicyRule) bool {
	for _, val := range rules {
		if reflect.DeepEqual(val, rule) {
			return true
		}
	}
	return false
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

func ExistContainerByName(name string, containers []corev1.Container) bool {
	for _, val := range containers {
		if val.Name == name {
			return true
		}
	}
	return false
}

func ExistVolumeMountsByName(name string, volumeMounts []corev1.VolumeMount) bool {
	for _, val := range volumeMounts {
		if val.Name == name {
			return true
		}
	}
	return false
}

func ExistVolumeMsByName(name string, volumes []corev1.Volume) bool {
	for _, val := range volumes {
		if val.Name == name {
			return true
		}
	}
	return false
}

func ExistPortsByPort(port int32, ports []corev1.ServicePort) bool {
	for _, val := range ports {
		if val.Port == port {
			return true
		}
	}
	return false
}

func (s *Server) ensureService() error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of Deployment before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		var result *corev1.Service
		var getErr error
		result, getErr = s.clientset.CoreV1().Services(s.args.CoreDNSNamespace).Get(context.TODO(), s.args.CoreDNSName, metav1.GetOptions{})
		if getErr != nil {
			result, getErr = s.clientset.CoreV1().Services(s.args.CoreDNSNamespace).Get(context.TODO(), "kube-dns", metav1.GetOptions{})
			if getErr != nil {
				return fmt.Errorf("failed to get latest version of Service: %v", getErr)
			}
		}
		if !ExistPortsByPort(s.args.ServerArgs.Port, result.Spec.Ports) {
			result.Spec.Ports = append(result.Spec.Ports, corev1.ServicePort{
				Name: "apis",
				Port: s.args.ServerArgs.Port,
			})
			_, updateErr := s.clientset.CoreV1().Services(s.args.CoreDNSNamespace).Update(context.TODO(), result, metav1.UpdateOptions{})
			return updateErr
		}
		return nil
	})
	return retryErr
}

func (s *Server) ensureCoreDNSConfigmap() error {
	cm, err := s.clientset.CoreV1().ConfigMaps(s.args.CoreDNSNamespace).Get(context.TODO(), s.args.CoreDNSName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	corefile, needUpdate, err := BuildNewCoreFile([]byte(cm.Data["Corefile"]))
	if err != nil {
		return err
	}
	klog.InfoS("The coreDNS config content", "corefile", string(corefile))
	if needUpdate {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of Deployment before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			var result *corev1.ConfigMap
			var getErr error
			result, getErr = s.clientset.CoreV1().ConfigMaps(s.args.CoreDNSNamespace).Get(context.TODO(), s.args.CoreDNSName, metav1.GetOptions{})
			if getErr != nil {
				return fmt.Errorf("failed to get latest version of ConfigMap: %v", getErr)
			}
			result.Data["Corefile"] = string(corefile)
			// update
			_, updateErr := s.clientset.CoreV1().ConfigMaps(s.args.CoreDNSNamespace).Update(context.TODO(), result, metav1.UpdateOptions{})
			return updateErr
		})
		return retryErr
	}
	return nil
}

const (
	filename  = "Caddyfile"
	hostsPath = "/etc/coredns-dir/hosts"
)

func BuildNewCoreFile(corefile []byte) ([]byte, bool, error) {
	var j caddyfile.EncodedCaddyfile
	var needUpdate bool
	serverBlocks, err := caddyfile.Parse(filename, bytes.NewReader(corefile), nil)
	if err != nil {
		return nil, needUpdate, err
	}

	for _, sb := range serverBlocks {
		block := caddyfile.EncodedServerBlock{
			Keys: sb.Keys,
			Body: [][]interface{}{},
		}
		// Extract directives deterministically by sorting them
		var hostsItem []interface{}
		hostsItem = append(hostsItem, "hosts")
		hostsItem = append(hostsItem, hostsPath)

		var directives = make([]string, len(sb.Tokens))
		for dir := range sb.Tokens {
			directives = append(directives, dir)
		}
		if !ExistStringSlice("hosts", directives) {
			directives = append(directives, "hosts")
		}
		sort.Strings(directives)

		// Convert each directive's tokens into our JSON structure
		for _, dir := range directives {
			// hosts 插件单独处理
			if dir == "hosts" {
				switch {
				case len(sb.Tokens[dir]) == 0:
					needUpdate = true
					block.Body = append(block.Body, hostsItem)
				default:
					disp := caddyfile.NewDispenserTokens(filename, sb.Tokens[dir])
					for disp.Next() {
						item := constructLine(&disp)
						// first floor
						if item[0] == "hosts" {
							if !ExistInterfaceSlice(hostsPath, item) {
								needUpdate = true
								if len(item) == 1 {
									item = append(item, hostsPath)
								} else {
									item[1] = hostsPath
								}
							}
						}
						block.Body = append(block.Body, item)
					}
				}
			} else {
				disp := caddyfile.NewDispenserTokens(filename, sb.Tokens[dir])
				for disp.Next() {
					item := constructLine(&disp)
					block.Body = append(block.Body, item)
				}
			}
		}
		// tack this block onto the end of the list
		j = append(j, block)
	}
	result, err := json.Marshal(j)
	if err != nil {
		return nil, needUpdate, err
	}
	// encode
	newResult, err := caddyfile.FromJSON(result)
	if err != nil {
		return nil, needUpdate, err
	}
	return newResult, needUpdate, nil
}

func ExistInterfaceSlice(val string, item []interface{}) bool {
	for _, v := range item {
		if val == v {
			return true
		}
	}
	return false
}

func ExistStringSlice(val string, item []string) bool {
	for _, v := range item {
		if val == v {
			return true
		}
	}
	return false
}

// constructLine transforms tokens into a JSON-encodable structure;
// but only one line at a time, to be used at the top-level of
// a server block only (where the first token on each line is a
// directive) - not to be used at any other nesting level.
func constructLine(d *caddyfile.Dispenser) []interface{} {
	var args []interface{}

	args = append(args, d.Val())

	for d.NextArg() {
		if d.Val() == "{" {
			args = append(args, constructBlock(d))
			continue
		}
		args = append(args, d.Val())
	}

	return args
}

// constructBlock recursively processes tokens into a
// JSON-encodable structure. To be used in a directive's
// block. Goes to end of block.
func constructBlock(d *caddyfile.Dispenser) [][]interface{} {
	var block [][]interface{}

	for d.Next() {
		if d.Val() == "}" {
			break
		}
		block = append(block, constructLine(d))
	}

	return block
}
