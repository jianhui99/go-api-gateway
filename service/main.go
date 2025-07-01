package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.etcd.io/etcd/clientv3"
)

type MicroService struct {
	name       string
	port       int
	etcdClient *clientv3.Client
	leaseID    clientv3.LeaseID
}

func NewMicroService(name string, port int, etcdEndpoints []string) (*MicroService, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdEndpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	return &MicroService{
		name:       name,
		port:       port,
		etcdClient: client,
	}, nil
}

// registerService 注册服务到etcd
func (ms *MicroService) registerService() error {
	// 创建租约（30秒）
	lease, err := ms.etcdClient.Grant(context.Background(), 30)
	if err != nil {
		return err
	}
	ms.leaseID = lease.ID

	// 服务信息
	serviceInfo := map[string]interface{}{
		"host":   "localhost",
		"port":   ms.port,
		"health": "/health",
	}

	infoBytes, _ := json.Marshal(serviceInfo)
	key := fmt.Sprintf("/services/%s/instance-%d", ms.name, ms.port)

	// 注册服务
	_, err = ms.etcdClient.Put(context.Background(), key, string(infoBytes), clientv3.WithLease(lease.ID))
	if err != nil {
		return err
	}

	// 保持租约活跃
	ch, err := ms.etcdClient.KeepAlive(context.Background(), lease.ID)
	if err != nil {
		return err
	}

	// 处理续约响应
	go func() {
		for resp := range ch {
			if resp != nil {
				log.Printf("Lease renewed: %x", resp.ID)
			}
		}
	}()

	log.Printf("Service %s registered at localhost:%d", ms.name, ms.port)
	return nil
}

// deregisterService 注销服务
func (ms *MicroService) deregisterService() {
	if ms.leaseID != 0 {
		ms.etcdClient.Revoke(context.Background(), ms.leaseID)
	}
	ms.etcdClient.Close()
	log.Printf("Service %s deregistered", ms.name)
}

// healthHandler 健康检查
func (ms *MicroService) healthHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"service": ms.name,
		"status":  "healthy",
		"port":    ms.port,
		"time":    time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// serviceHandler 模拟业务逻辑
func (ms *MicroService) serviceHandler(w http.ResponseWriter, r *http.Request) {
	var body interface{}
	if r.Body != nil {
		defer r.Body.Close()
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil && err != io.EOF {
			body = fmt.Sprintf("Failed to decode body: %v", err)
		}
	}

	response := map[string]interface{}{
		"service": ms.name,
		"method":  r.Method,
		"path":    r.URL.Path,
		"header":  r.Header,
		"body":    body,
		"message": fmt.Sprintf("Hello from %s service!", ms.name),
		"time":    time.Now().Format(time.RFC3339),
	}

	// 模拟一些处理时间
	time.Sleep(100 * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (ms *MicroService) start() {
	// 注册路由
	http.HandleFunc("/health", ms.healthHandler)
	http.HandleFunc("/", ms.serviceHandler)

	// 启动HTTP服务器
	server := &http.Server{
		Addr: fmt.Sprintf(":%d", ms.port),
	}

	go func() {
		log.Printf("%s service starting on port %d...", ms.name, ms.port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("Server failed to start:", err)
		}
	}()

	// 注册到etcd
	if err := ms.registerService(); err != nil {
		log.Fatal("Failed to register service:", err)
	}

	// 等待信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// 注销服务
	ms.deregisterService()

	// 优雅关闭HTTP服务器
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)

	log.Println("Server stopped")
}

func main() {
	if len(os.Args) < 3 {
		log.Fatal("Usage: go run service.go <service-name> <port>")
	}

	serviceName := os.Args[1]
	port, err := strconv.Atoi(os.Args[2])
	if err != nil {
		log.Fatal("Invalid port:", os.Args[2])
	}

	service, err := NewMicroService(serviceName, port, []string{"localhost:2379"})
	if err != nil {
		log.Fatal("Failed to create service:", err)
	}

	service.start()
}
