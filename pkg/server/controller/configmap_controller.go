package controller

import (
	"context"
	"fmt"
	"k8s.io/klog/v2"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	ConcurrentConfigmapSyncs = 1

	ConfigmapName      = "coredns-hosts-api"
	ConfigmapNamespace = "kube-system"
)

type ConfigmapController struct {
	clientset       *kubernetes.Clientset
	configmapLister corelisters.ConfigMapLister
	configmapSynced cache.InformerSynced
	filePath        string

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
}

func NewConfigmapController(clientset *kubernetes.Clientset, configmapInformer coreinformers.ConfigMapInformer, filePath string) *ConfigmapController {
	c := &ConfigmapController{
		clientset:       clientset,
		configmapLister: configmapInformer.Lister(),
		configmapSynced: configmapInformer.Informer().HasSynced,
		filePath:        filePath,

		workqueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Configmap"),
	}

	configmapInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cm := obj.(*corev1.ConfigMap)
			if c.FilterConfigmap(cm) {
				klog.InfoS("Add Event", "configmap", klog.KObj(cm))
				c.enqueue(cm)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			cm, ok := newObj.(*corev1.ConfigMap)
			oldCm, ok1 := oldObj.(*corev1.ConfigMap)
			if ok && ok1 && cm.ResourceVersion != oldCm.ResourceVersion {
				if c.FilterConfigmap(cm) {
					klog.InfoS("Update Event", "configmap", klog.KObj(cm))
					c.enqueue(cm)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			// 处理下
		},
	})

	return c
}

func (c *ConfigmapController) Run(stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting configmap controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.configmapSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch once workers to process ConfigMap resources
	for i := 1; i <= ConcurrentConfigmapSyncs; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

func (c *ConfigmapController) FilterConfigmap(cm *corev1.ConfigMap) bool {
	if cm.Name == ConfigmapName && cm.Namespace == ConfigmapNamespace {
		return true
	}
	return false
}

func (c *ConfigmapController) enqueue(cm *corev1.ConfigMap) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(cm)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cm, err))
		return
	}

	c.workqueue.Add(key)
}

func (c *ConfigmapController) worker() {
	for {
		func() {
			key, quit := c.workqueue.Get()
			if quit {
				return
			}
			defer c.workqueue.Done(key)
			startTime := time.Now()
			err := c.syncConfigmap(key.(string))
			if err != nil {
				klog.ErrorS(err, "Error syncing configmap and retry...", "node", key)
				c.workqueue.AddRateLimited(key)
			} else {
				c.workqueue.Forget(key)
				klog.Infof("Finished syncing configmap(%s), and cost %s", key, time.Now().Sub(startTime))
			}
		}()
	}
}

func (c *ConfigmapController) syncConfigmap(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	cm, err := c.clientset.CoreV1().ConfigMaps(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	switch {
	case errors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	default:
		var content string
		for key, val := range cm.Data {
			item := fmt.Sprintf("%s %s\n", val, key)
			content += item
		}
		return os.WriteFile(c.filePath, []byte(content), 0644)
	}
}
