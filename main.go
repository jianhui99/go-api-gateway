package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/clientv3"
)

type ServiceInstance struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Health string `json:"health"`
}

type Gateway struct {
	etcdClient *clientv3.Client
	services   map[string][]*ServiceInstance
	mutex      sync.RWMutex
	routes     map[string]string
}

func NewGateway(etcdEndpoints []string) (*Gateway, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdEndpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	gw := &Gateway{
		etcdClient: client,
		services:   make(map[string][]*ServiceInstance),
		routes: map[string]string{
			"/api/logs":     "log-service",
			"/api/messages": "message-service",
			"/api/users":    "user-service",
			"/api/orders":   "order-service",
			"/api/payments": "payment-service",
		},
	}

	// 启动服务发现
	go gw.watchServices()

	// 初始加载服务
	gw.loadServices()

	return gw, nil
}

// loadServices 从etcd加载所有服务
func (gw *Gateway) loadServices() {
	resp, err := gw.etcdClient.Get(context.Background(), "/services/", clientv3.WithPrefix())
	if err != nil {
		log.Printf("Failed to load services: %v", err)
		return
	}

	for _, kv := range resp.Kvs {
		gw.updateService(string(kv.Key), string(kv.Value))
	}
}

// watchServices 监听etcd中的服务变化
func (gw *Gateway) watchServices() {
	watchChan := gw.etcdClient.Watch(context.Background(), "/services/", clientv3.WithPrefix())

	for watchResp := range watchChan {
		for _, event := range watchResp.Events {
			key := string(event.Kv.Key)

			switch event.Type {
			case clientv3.EventTypePut:
				gw.updateService(key, string(event.Kv.Value))
			case clientv3.EventTypeDelete:
				gw.removeService(key)
			}
		}
	}
}

// updateService 更新服务实例
func (gw *Gateway) updateService(key, value string) {
	// 解析key: /services/log-service/instance-1
	parts := strings.Split(key, "/")
	if len(parts) < 3 {
		return
	}

	serviceName := parts[2]

	var instance ServiceInstance
	if err := json.Unmarshal([]byte(value), &instance); err != nil {
		log.Printf("Failed to unmarshal service instance: %v", err)
		return
	}

	gw.mutex.Lock()
	defer gw.mutex.Unlock()

	if gw.services[serviceName] == nil {
		gw.services[serviceName] = make([]*ServiceInstance, 0)
	}

	// 检查是否已存在，如果存在则更新，否则添加
	found := false
	for i, inst := range gw.services[serviceName] {
		if inst.Host == instance.Host && inst.Port == instance.Port {
			gw.services[serviceName][i] = &instance
			found = true
			break
		}
	}

	if !found {
		gw.services[serviceName] = append(gw.services[serviceName], &instance)
	}

	log.Printf("Updated service %s: %s:%d", serviceName, instance.Host, instance.Port)
}

// removeService 移除服务实例
func (gw *Gateway) removeService(key string) {
	parts := strings.Split(key, "/")
	if len(parts) < 3 {
		return
	}

	serviceName := parts[2]

	gw.mutex.Lock()
	defer gw.mutex.Unlock()

	// 简单实现：直接清空该服务的所有实例
	// 实际应用中需要更精确的实例管理
	delete(gw.services, serviceName)

	log.Printf("Removed service %s", serviceName)
}

// getServiceInstance 获取服务实例（简单轮询负载均衡）
func (gw *Gateway) getServiceInstance(serviceName string) *ServiceInstance {
	gw.mutex.RLock()
	defer gw.mutex.RUnlock()

	instances := gw.services[serviceName]
	if len(instances) == 0 {
		return nil
	}

	// 简单轮询（实际应该用更复杂的负载均衡算法）
	now := time.Now().Unix()
	index := int(now) % len(instances)
	return instances[index]
}

// ServeHTTP 实现http.Handler接口
func (gw *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 添加CORS头
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 路由匹配
	serviceName := gw.matchRoute(r.URL.Path)
	if serviceName == "" {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	// 获取服务实例
	instance := gw.getServiceInstance(serviceName)
	if instance == nil {
		http.Error(w, "No available service instance", http.StatusServiceUnavailable)
		return
	}

	// 创建反向代理
	target := fmt.Sprintf("http://%s:%d", instance.Host, instance.Port)
	proxy := gw.createReverseProxy(target, serviceName)

	log.Printf("Proxying %s %s to %s", r.Method, r.URL.Path, target)
	proxy.ServeHTTP(w, r)
}

// matchRoute 路由匹配
func (gw *Gateway) matchRoute(path string) string {
	for prefix, serviceName := range gw.routes {
		if strings.HasPrefix(path, prefix) {
			return serviceName
		}
	}
	return ""
}

// createReverseProxy 创建反向代理
func (gw *Gateway) createReverseProxy(target, serviceName string) *httputil.ReverseProxy {
	url, _ := url.Parse(target)

	proxy := httputil.NewSingleHostReverseProxy(url)

	// 自定义Director来修改请求
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// 移除路径前缀
		for prefix := range gw.routes {
			if strings.HasPrefix(req.URL.Path, prefix) {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
				if req.URL.Path == "" {
					req.URL.Path = "/"
				}
				break
			}
		}

		// 添加自定义头
		req.Header.Set("X-Gateway", "simple-go-gateway")
		req.Header.Set("X-Service", serviceName)
	}

	// 错误处理
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error: %v", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	return proxy
}

// healthCheck 健康检查端点
func (gw *Gateway) healthCheck(w http.ResponseWriter, r *http.Request) {
	gw.mutex.RLock()
	defer gw.mutex.RUnlock()

	status := map[string]interface{}{
		"status":   "healthy",
		"services": make(map[string]int),
	}

	for serviceName, instances := range gw.services {
		status["services"].(map[string]int)[serviceName] = len(instances)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// listServices 列出所有服务
func (gw *Gateway) listServices(w http.ResponseWriter, r *http.Request) {
	gw.mutex.RLock()
	defer gw.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(gw.services)
}

func main() {
	// 创建网关实例
	gateway, err := NewGateway([]string{"localhost:2379"})
	if err != nil {
		log.Fatal("Failed to create gateway:", err)
	}
	defer gateway.etcdClient.Close()

	// 设置路由
	http.Handle("/", gateway)
	http.HandleFunc("/gateway/health", gateway.healthCheck)
	http.HandleFunc("/gateway/services", gateway.listServices)

	// 启动服务器
	log.Println("API Gateway starting on :8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
