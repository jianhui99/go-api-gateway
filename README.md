# 用golang简单实现的微服务网关

## 示例图
```
客户端 -> HTTP -> 网关 -> 服务
```

## 启动网关：(main.go)
# 1. 启动etcd: docker-compose up -d etcd
# 2. 注册服务:
#    docker exec etcd etcdctl put /services/log-service/instance-1 '{"host":"localhost","port":8001,"health":"/health"}'
#    docker exec etcd etcdctl put /services/message-service/instance-1 '{"host":"localhost","port":8002,"health":"/health"}'
# 3. 启动网关: go run main.go
# 4. 测试: curl http://localhost:8080/api/logs/test


## 启动服务：(在service目录下)    
# 启动日志服务: go run main.go log-service 8001
# 启动消息服务: go run main.go message-service 8002
# 启动用户服务: go run main.go user-service 8003


# etcd 基本操作

# 启动etcd
docker-compose up -d etcd

# 查看etcd状态
docker exec etcd etcdctl endpoint health

# 查看集群成员
docker exec etcd etcdctl member list

# 设置键值
docker exec etcd etcdctl put /test/key "test-value"

# 获取键值
docker exec etcd etcdctl get /test/key

# 获取所有键
docker exec etcd etcdctl get --prefix /

# 删除键
docker exec etcd etcdctl del /test/key

# 监听键变化
docker exec etcd etcdctl watch /test/key
