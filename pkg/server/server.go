package server

import (
	"context"
	"fmt"
	"github.com/devincd/coredns-hosts-api/pkg/server/controller"
	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

type Server struct {
	clientset           *kubernetes.Clientset
	webServer           *http.Server
	configmapController *controller.ConfigmapController
	informerFactory     informers.SharedInformerFactory
}

func NewServer(args Args) (*Server, error) {
	s := &Server{}
	if err := s.initKubeClient(args); err != nil {
		return nil, err
	}
	s.initController()
	if err := s.initWebService(args); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Run(stop chan struct{}) error {
	klog.Info("start the service")

	// notice that there is no need to run start methods in a separate goroutine.
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	s.informerFactory.Start(stop)
	// Run the configmap controller component
	go func() {
		err := s.configmapController.Run(stop)
		if err != nil {
			klog.Fatalf("Error running configmap controller: %v", err)
		}
	}()
	// Run the http server component
	go func() {
		err := s.webServer.ListenAndServe()
		if err != nil {
			klog.Fatalf("Error running http server: %v", err)
		}
	}()
	return nil
}

func (s *Server) initWebService(args Args) error {
	route := gin.Default()
	route.Use()

	record, err := newRecordController(s.clientset)
	if err != nil {
		return err
	}
	apiv1 := route.Group("/api/v1")
	{
		apiv1.POST("/records", record.PostRecords)
		apiv1.DELETE("/records", record.DeleteRecords)
		apiv1.GET("/records", record.ListRecords)
		apiv1.GET("record/:domain", record.GetRecord)
	}

	webServer := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", args.Port),
		Handler: route,
	}
	s.webServer = webServer

	return nil
}

// initKubeClient creates the k8s client if running in a k8s environment.
func (s *Server) initKubeClient(args Args) error {
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

func (s *Server) initController() {
	informerFactory := informers.NewSharedInformerFactory(s.clientset, 0)
	s.informerFactory = informerFactory

	s.configmapController = controller.NewConfigmapController(s.clientset, s.informerFactory.Core().V1().ConfigMaps())
}

type recordController struct {
	// 自定义记录的数据存放地
	// key = 域名
	// value = IP
	lock      *sync.RWMutex
	clientset *kubernetes.Clientset
}

func newRecordController(clientset *kubernetes.Clientset) (*recordController, error) {
	rc := &recordController{
		lock:      &sync.RWMutex{},
		clientset: clientset,
	}
	err := rc.initConfigmap()
	if err != nil {
		return rc, err
	}
	return rc, nil
}

func (r *recordController) initConfigmap() error {
	_, err := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Get(context.TODO(), controller.ConfigmapName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			newCm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      controller.ConfigmapName,
					Namespace: controller.ConfigmapNamespace,
				},
				Data: make(map[string]string),
			}
			_, err := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Create(context.TODO(), newCm, metav1.CreateOptions{})
			return err
		}
		return err
	}
	return nil
}

func (r *recordController) SetData(domain, ip string) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of Deployment before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		cm, getErr := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Get(context.TODO(), controller.ConfigmapName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("failed to get latest version of Configmap: %v", getErr)
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		// If the record is existed and ignore
		if val, ok := cm.Data[domain]; ok {
			if val == ip {
				return nil
			}
		}
		cm.Data[domain] = ip
		newCm, updateErr := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
		if updateErr != nil {
			return updateErr
		}
		// Check again
		if newCm.Data == nil {
			return fmt.Errorf("failed to setData and updateCm's Data is nil, domainInfo is %s(%s)", domain, ip)
		}
		if newCm.Data[domain] != ip {
			return fmt.Errorf("failed to setData and updateCm's value is not right, domainInfo is %s(%s)", domain, ip)
		}
		return nil
	})
	return retryErr
}

func (r *recordController) DeleteData(domain string) error {
	r.lock.Lock()
	defer r.lock.Unlock()
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of Deployment before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		cm, getErr := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Get(context.TODO(), controller.ConfigmapName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("failed to get latest version of Configmap: %v", getErr)
		}
		if cm.Data == nil || len(cm.Data) == 0 {
			return nil
		}
		// If the record is not existed and ignore
		if _, ok := cm.Data[domain]; !ok {
			return nil
		}
		delete(cm.Data, domain)
		newCm, updateErr := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Update(context.TODO(), cm, metav1.UpdateOptions{})
		if updateErr != nil {
			return updateErr
		}
		// Check again
		if newCm.Data == nil || len(newCm.Data) == 0 {
			return nil
		}
		if val, ok := newCm.Data[domain]; ok {
			return fmt.Errorf("failed to DeleteData and updateCm's val is exist, domainInfo is %s(%s)", domain, val)
		}
		return nil
	})
	return retryErr
}

func (r *recordController) GetDatas() ([]*Record, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	ret := make([]*Record, 0)
	cm, err := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Get(context.TODO(), controller.ConfigmapName, metav1.GetOptions{})
	if err != nil {
		return ret, err
	}
	for k, v := range cm.Data {
		item := &Record{
			Domain: k,
			IP:     v,
		}
		ret = append(ret, item)
	}
	return ret, nil
}

func (r *recordController) GetData(domain string) (*Record, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	ret := &Record{}
	cm, err := r.clientset.CoreV1().ConfigMaps(controller.ConfigmapNamespace).Get(context.TODO(), controller.ConfigmapName, metav1.GetOptions{})
	if err != nil {
		return ret, err
	}
	if ip, ok := cm.Data[domain]; ok {
		ret.Domain = domain
		ret.IP = ip
	} else {
		return ret, fmt.Errorf("can't find the ip according to the domain %s", domain)
	}
	return ret, nil
}

// Record for PostRecords function
type Record struct {
	IP     string `json:"ip" binding:"required"`
	Domain string `json:"domain" binding:"required"`
}

// DeleteRecord for DeleteRecords function
type DeleteRecord struct {
	IP     string `json:"ip"`
	Domain string `json:"domain" binding:"required"`
}

func (r *recordController) PostRecords(c *gin.Context) {
	var record Record
	if err := c.ShouldBindJSON(&record); err != nil {
		klog.ErrorS(err, "Response with a error", "httpCode", http.StatusBadRequest, "requestUri", c.Request.RequestURI)
		c.JSON(http.StatusBadRequest, ErrorResponse(err))
		return
	}
	err := r.SetData(record.Domain, record.IP)
	if err != nil {
		klog.ErrorS(err, "Response with a error", "httpCode", http.StatusInternalServerError, "requestUri", c.Request.RequestURI)
		c.JSON(http.StatusInternalServerError, ErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, SuccessResponse(nil, fmt.Sprintf("PostRecords is successful. Domain is %s, and ip is %s", record.Domain, record.IP)))
}

func (r *recordController) DeleteRecords(c *gin.Context) {
	var record DeleteRecord
	if err := c.ShouldBindJSON(&record); err != nil {
		klog.ErrorS(err, "Response with a error", "httpCode", http.StatusBadRequest, "requestUri", c.Request.RequestURI)
		c.JSON(http.StatusBadRequest, ErrorResponse(err))
		return
	}
	err := r.DeleteData(record.Domain)
	if err != nil {
		klog.ErrorS(err, "Response with a error", "httpCode", http.StatusInternalServerError, "requestUri", c.Request.RequestURI)
		c.JSON(http.StatusInternalServerError, ErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, SuccessResponse(nil, fmt.Sprintf("DeleteRecords is successful. Domain is %s, and ip is %s", record.Domain, record.IP)))
}

func (r *recordController) ListRecords(c *gin.Context) {
	ret, err := r.GetDatas()
	if err != nil {
		klog.ErrorS(err, "Response with a error", "httpCode", http.StatusInternalServerError, "requestUri", c.Request.RequestURI)
		c.JSON(http.StatusInternalServerError, ErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, SuccessResponse(ret, "ListRecords is successful."))
}

func (r *recordController) GetRecord(c *gin.Context) {
	domain := c.Param("domain")

	ret, err := r.GetData(domain)
	if err != nil {
		klog.ErrorS(err, "Response with a error", "httpCode", http.StatusInternalServerError, "requestUri", c.Request.RequestURI)
		c.JSON(http.StatusInternalServerError, ErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, SuccessResponse(ret, fmt.Sprintf("GetRecord is successful. Domain is %s", domain)))
}

func FileExist(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

type Response struct {
	// 统一状态码，成功=0 失败>0
	Code    int         `json:"code"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

// SuccessResponse for success response
func SuccessResponse(data interface{}, msg string) *Response {
	if msg == "" {
		msg = "operate successfully"
	}
	return &Response{
		Code:    0,
		Data:    data,
		Message: msg,
	}
}

func ErrorResponse(err error) *Response {
	return &Response{
		Code:    1,
		Data:    nil,
		Message: err.Error(),
	}
}
