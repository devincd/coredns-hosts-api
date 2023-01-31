# coredns-hosts-api
Implement API to add and delete DNS records based on coreDNS hosts plugin

## 原理
>kube-system 命名空间下会自动创建名字为 coredns-hosts-api 的 configmap，用于存储自定义的 DNS 记录。

## 自动安装
运行一次性脚本
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: coredns-hosts-installer
  namespace: kube-system

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: system:coredns-hosts-installer
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: coredns-hosts-installer
    namespace: kube-system

---	
apiVersion: batch/v1
kind: Job
metadata:
  name: coredns-hosts-installer
  namespace: kube-system
spec:
  template:
    spec:
      serviceAccountName: coredns-hosts-installer
      containers:
      - name: coredns-hosts-installer
        image: docker.io/devincd/coredns-hosts-installer:v1.0.0
        imagePullPolicy: Always
      restartPolicy: Never
  backoffLimit: 4
```

## 手动安装
前提条件，由于需要操作 configmap，所以需要修改下 clusterrole，完整的 clusterrole如下：
```yaml
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRole
metadata:
  labels:
    kubernetes.io/bootstrapping: rbac-defaults
  name: system:coredns
rules:
- apiGroups:
  - ""
  resources:
  - endpoints
  - services
  - pods
  - namespaces
  verbs:
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - nodes
  verbs:
  - get
## 新增yaml
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - *
## 新增yaml结束
```

第一步：coredns-hosts-server 以 sidecar 的形式注入到 coredns deployment 中去，
那么完整的 coredns deployment 如下：
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coredns
  namespace: kube-system
  labels:
    k8s-app: coredns
    kubernetes.io/name: "CoreDNS"
spec:
  replicas: 2
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
  selector:
    matchLabels:
      k8s-app: coredns
  template:
    metadata:
      labels:
        k8s-app: coredns
    spec:
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchExpressions:
              - key: k8s-app
                operator: In
                values:
                - coredns
            topologyKey: kubernetes.io/hostname
      serviceAccountName: coredns
      nodeSelector:
        kubernetes.io/role: "master"
      tolerations:
      - operator: Exists
      - key: "CriticalAddonsOnly"
        operator: "Exists"
      containers:
      ## 新增yaml
      - name: coredns-hosts-server
        image: docker.io/devincd/coredns-hosts-server:v1.0.0
        imagePullPolicy: IfNotPresent
        volumeMounts:
          - mountPath: /etc/coredns-dir
            name: shared-data
        ports:
          - containerPort: 9080 
      ## 新增yaml结束       
      - name: coredns
        image: coredns/coredns:1.9.4
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: 100m
            memory: 70Mi
        args: [ "-conf", "/etc/coredns/Corefile" ]
        volumeMounts:
        - name: config-volume
          mountPath: /etc/coredns
          readOnly: true
        - name: run
          mountPath: /run
          readOnly: true
        ## 新增yaml  
        - name: shared-data
          mountPath: /etc/coredns-dir
        ## 新增yaml结束
        ports:
        - containerPort: 53
          name: dns
          protocol: UDP
        - containerPort: 53
          name: dns-tcp
          protocol: TCP
        - containerPort: 9153
          name: metrics
          protocol: TCP
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            add:
            - NET_BIND_SERVICE
            drop:
            - all
          readOnlyRootFilesystem: true
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
            scheme: HTTP
          initialDelaySeconds: 60
          timeoutSeconds: 5
          successThreshold: 1
          failureThreshold: 5
      dnsPolicy: Default
      volumes:
        - name: config-volume
          configMap:
            name: coredns
            items:
            - key: Corefile
              path: Corefile
        - hostPath:
            path: /run
          name: run
        ## 新增yaml
        - name: shared-data
          emptyDir: {}
        ## 新增yaml结束
```

第二步：将接口服务通过 coredns svc 暴露出去，那么完整的 coredns svc 为：
```yaml
apiVersion: v1
kind: Service
metadata:
  name: coredns
  namespace: kube-system
  annotations:
    prometheus.io/port: "9153"
    prometheus.io/scrape: "true"
  labels:
    k8s-app: coredns
    kubernetes.io/cluster-service: "true"
    kubernetes.io/name: "CoreDNS"
spec:
  selector:
    k8s-app: coredns
  ports:
  - name: dns
    port: 53
    protocol: UDP
  - name: dns-tcp
    port: 53
    protocol: TCP
  - name: metrics
    port: 9153
    protocol: TCP
  ## 新增yaml  
  - name: apis
    port: 9080
    protocol: TCP
  ## 新增yaml结束
```

第三步：修改 coreDNS configmap 配置
```yaml
.:53 {
        errors
        health
        kubernetes cluster.local in-addr.arpa ip6.arpa {
          pods insecure
          fallthrough in-addr.arpa ip6.arpa
        }
        # hosts can add hosts's item into dns, see https://coredns.io/plugins/hosts/
        ## 新增了hosts插件的file文件
        hosts /etc/coredns-dir/hosts {
            112.80.248.75 www.baidu.com
            fallthrough
        }
        prometheus :9153
        forward . /etc/resolv.conf
        cache 30
        loop
        reload
        loadbalance
    }
```

## 接口示例（无论成功还是失败，返回的http状态码都是200）
### 添加或则更新自定义记录
```shell
$ curl -X POST \
  http://corednsIP:9080/api/v1/records \
  -d '{
	"domain": "www.baidu.com",
	"ip": "1.1.2.4"
}'
{"code":0,"data":null,"message":"operate successfully"}
```

### 查找自定义记录(只返回通过 coredns-hosts-api 创建的 DNS 记录)
```shell
### 返回所有自定义记录
$ curl -X GET http://corednsIP:9080/api/v1/records
{"code":0,"data":[{"ip":"1.1.2.4","domain":"www.baidu.com"},{"ip":"1.1.2.3","domain":"www.youtubu.com"}],"message":"operate successfully"}

### 返回指定自定义记录
$ curl -X GET http://corednsIP:9080/api/v1/record/www.baidu.com
{"code":0,"data":{"ip":"1.1.2.4","domain":"www.baidu.com"},"message":"operate successfully"}
```

### 删除自定义记录
```shell
$ curl -X DELETE \
  http://corednsIP:9080/api/v1/records \
  -d '{
	"domain": "www.baidu.com",
	"ip": "1.1.2.4"
}'
{"code":0,"data":null,"message":"operate successfully"}
```

### 错误请求示例
```shell
$ curl -X GET http://corednsIP:9080/api/v1/record/www.baidu.com
{"code":1,"data":null,"message":"can't find the ip according to the domain www.baidu.com"}
```
