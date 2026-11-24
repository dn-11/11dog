# 11Dog — DN11 网络测速与拨测平台 (ITDog Clone)

**11Dog** 是专为 **DN11** Overlay 网络设计的分布式网络测试平台，界面与交互贴近 `itdog.cn`，降低使用与学习成本。

## 核心架构

```
浏览器 (Vue 3 SPA)
   │  POST /api/v1/tasks + GET /api/v1/tasks/{id}/stream (SSE)
   ▼
11dog-server (Go HTTP/SSE + Reverse gRPC Hub)
   ├── IP 限流器 & 参数/CIDR 校验
   ├── DN11 前缀库 (ASN / Owner / Region 自动标注)
   └── 消息队列缓冲层 (NATS JetStream / Bounded Memory)
         ├── 每节点独立有界队列 (tasks.node.<node_id>, MaxMsgsPerSubject)
         ├── 任务 TTL 自动过期清理，队列满直接返回 过载保护 (overloaded)
         └── 信用流控分发器 (Credit-Based Dispatcher)
               │
               ▼ (Agent 主动反连控制端 gRPC Bi-directional Stream)
11dog-agent (纯 Go 静态二进制，无 CGO，systemd 托管)
   ├── 本地并发信号量硬保护 (max_concurrency)
   └── 纯 Go 探测引擎: ICMP Ping / TCPing / HTTP(S) / Traceroute / DNS
```

### 为什么采用「反连 gRPC + 消息队列缓冲」？
- **反连 gRPC (`Connect(stream AgentMessage) returns (stream ServerMessage)`)**：DN11 边缘节点往往位于 NAT 后或没有入站公网策略，Agent 主动拨号到 Server 并维持长连接（带 Keepalive 与指数退避重连），无需对边缘节点暴露入站端口。
- **多层边缘过载保护**：
  1. HTTP 层按客户端 IP 令牌桶限流；
  2. MQ 层为每个节点维护**有界 FIFO 缓冲队列**（NATS JetStream `MaxMsgsPerSubject` + `DiscardNewPerSubject` 或内存有界队列），突发流量先在队列缓冲，超出上限直接触发 `overloaded` 保护，不影响其他节点；
  3. 任务带截止时间（TTL），若节点离线或积压过久，在服务端直接淘汰（`expired`）而不会在节点恢复后造成雪崩；
  4. 服务端信用流控分发器 + Agent 本地并发信号量双重限制并发，保护边缘节点 CPU 与带宽。

## 快速本地运行

### 1. 构建前端并启动 Server（单机内存队列模式）

```bash
cd web && npm install && npm run build && cd ..
go run ./cmd/server --config deploy/server.yaml.example
```

访问 `http://127.0.0.1:8080`。

### 2. 在 `/admin` 创建节点并启动 Edge Agent

节点身份（名称、区域、网络组、ASN、最大并发）统一存储在服务端数据库（支持内置纯 Go `sqlite` 与 `postgres`），Agent 仅凭后台签发的专属通信 Token 反连鉴权：

1. 打开 `http://127.0.0.1:8080/admin` 并输入 `admin_token`（示例配置为 `change-me-dn11-admin-token`）；
2. 点击 **「+ 新增节点」** 创建节点并复制生成的一键安装命令（或专属 Token）；
3. 本地运行 Agent（只需指定 `--server` 和 `--token`）：

```bash
go run ./cmd/agent --server 127.0.0.1:9090 --token dog_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

## 边缘节点部署（服务端托管脚本 + systemd）

`11dog-server` 内置静态安装脚本与二进制分发接口（`GET /scripts/install.sh` 与 `GET /downloads/11dog-agent-linux-{amd64,arm64}`）：

- 在管理后台点击节点行的 **「部署脚本」**，直接复制如下一键命令在 Linux 边缘机运行：

```bash
curl -fsSL "https://11dog.dn11.example/scripts/install.sh?token=dog_xxx&grpc=11dog.dn11.example:9090" | sudo bash
```

- 卸载命令：`curl -fsSL "https://11dog.dn11.example/scripts/install.sh?uninstall=1" | sudo bash`
- `11dog-agent.service` 默认以非 root 用户 `11dog` 运行，并通过 `AmbientCapabilities=CAP_NET_RAW` 授予原生的 ICMP Ping 与 Traceroute 权限。

## Server Helm 部署（Kubernetes）

Chart 位于 `deploy/helm/11dog`，默认使用内置 NATS JetStream 有状态缓冲队列及 SQLite PVC（多副本高可用请切换 `db.driver=postgres`）：

```bash
# 校验与渲染测试
helm lint deploy/helm/11dog

# 安装（开启 NATS JetStream 缓冲 + NodePort 暴露 gRPC 给集群外 DN11 Agent）
helm upgrade --install 11dog ./deploy/helm/11dog \
  --namespace 11dog --create-namespace \
  --set auth.adminToken="your-strong-admin-token" \
  --set service.grpcExternal.enabled=true \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=11dog.dn11.example \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

> [!IMPORTANT]
> Chart 默认使用 **Traefik** 作为 IngressClass（已配置 `web`/`websecure` entrypoint 与 `kubernetes.io/h2c` gRPC `appProtocol`），默认镜像为 `ghcr.io/dn-11/11dog:latest`。若改用 Nginx Ingress，请关闭缓冲 (`proxy-buffering: "off"`) 以确保 SSE 实时流不被缓冲。

## 管理后台与前台隔离

- **前台测速页**（`/`）：仅展示面向普通用户的 Ping / TCPing / HTTP / Traceroute / DNS 测速界面及监测点地理位置，不暴露后台数据库、MQ 状态、节点硬件负载、IP 或接入配置。
- **管理后台**（`/admin`）：通过 `auth.admin_token`（或环境变量 `DOG_ADMIN_TOKEN`）鉴权，支持查看系统与 MQ 状态、在数据库中新增/编辑/启停节点、重置通信 Token、以及直接复制服务端托管的一键安装脚本。

## 可观测性与测试

- 健康检查：`GET /healthz`、`GET /readyz`
- Prometheus 指标：`GET /metrics`（含 `dog_nodes_online`、`dog_subtasks_overloaded_total`、`dog_node_queue_depth` 等）
- 运行全部 Go 单元测试与端到端集成测试：`go test -v ./...`
