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

> [!NOTE]
> 本地模式下若要测试后台的一键安装脚本，需先执行 `make build-agent-linux` 生成 `./bin/11dog-agent-linux-{amd64,arm64}`（对应示例配置中的 `agent_bin_dir: "./bin"`）；容器镜像已内置这两个二进制。

## 边缘节点部署（服务端托管脚本 + systemd）

`11dog-server` 内置安装脚本与 Agent 二进制分发接口（`GET /scripts/install.sh` 与 `GET /downloads/11dog-agent-linux-{amd64,arm64}`）。

在管理后台点击节点行的 **「部署脚本」**，复制生成的一键命令在 Linux 边缘机（amd64 / arm64）上运行。命令中的 gRPC 地址与 TLS 开关默认取自服务端配置 `public_grpc_addr` / `public_grpc_tls`（Helm 对应 `publicGRPCAddr` / `publicGRPCTLS`），例如 gRPC 经 Ingress 以 443 + TLS 暴露时：

```bash
curl -fsSL "https://11dog.dn11.example/scripts/install.sh?token=dog_xxx&grpc=grpc.11dog.dn11.example:443&tls=true" | sudo bash
```

- 卸载命令：`curl -fsSL "https://11dog.dn11.example/scripts/install.sh?uninstall=1" | sudo bash`
- Agent 配置写入 `/etc/11dog/agent.yaml`，日志查看：`journalctl -u 11dog-agent -f`
- `11dog-agent.service` 默认以非 root 用户 `11dog` 运行，并通过 `AmbientCapabilities=CAP_NET_RAW` 授予原生的 ICMP Ping 与 Traceroute 权限。

## Server Helm 部署（Kubernetes）

Chart 以 OCI 制品形式托管在 GHCR：`oci://ghcr.io/dn-11/charts/11dog`（推送 `vX.Y.Z` tag 时由 CI 自动发布，Chart 版本为 `X.Y.Z`，并固定使用同 tag 的镜像 `ghcr.io/dn-11/11dog:vX.Y.Z`）。

默认使用内置 NATS JetStream 缓冲队列 + SQLite PVC（单副本）；多副本高可用请切换 `db.driver=postgres` 并调大 `replicaCount`。

Chart 只创建一个 Service（默认 `ClusterIP`，可通过 `service.type` / `service.annotations` 调整），**不包含 Ingress**，对外暴露方式请按自己的集群自行配置：

| 端口 | 名称 | 协议 | 用途 |
| --- | --- | --- | --- |
| `8080` | `http` | HTTP/1.1 | Web 前台、`/admin` 后台、API、SSE 实时流、`/scripts/install.sh`、`/downloads/*` |
| `9090` | `grpc` | gRPC（h2c 明文；`tls.enabled=true` 时为 TLS） | Agent 反连长连接 |

```bash
# 查看默认 values
helm show values oci://ghcr.io/dn-11/charts/11dog > values.yaml

# 安装 / 升级（可加 --version X.Y.Z 固定版本，不加则安装最新正式版）
helm upgrade --install dn11dog oci://ghcr.io/dn-11/charts/11dog \
  --namespace 11dog --create-namespace \
  --set auth.adminToken="your-strong-admin-token" \
  --set publicGRPCAddr=grpc.11dog.dn11.example:443 \
  --set publicGRPCTLS=true
```

安装后 Service 为 `dn11dog.11dog.svc`（Release 名包含 `11dog` 时直接用作资源名，否则为 `<release>-11dog`）。未配置 Ingress 前可 `kubectl -n 11dog port-forward svc/dn11dog 8080:8080` 访问 `http://127.0.0.1:8080/admin`。

`publicGRPCAddr` / `publicGRPCTLS` 填 Agent 实际能连到的 gRPC 入口（例如你自己配置的 gRPC Ingress `域名:443` + TLS），后台生成的一键安装命令会自动带上；留空则默认 `<访问域名>:9090` 明文。

从源码目录安装（开发调试）：

```bash
helm lint deploy/helm/11dog
helm upgrade --install dn11dog ./deploy/helm/11dog -n 11dog --create-namespace -f values.yaml
```

> [!IMPORTANT]
> - Release 名需以字母开头（Service 名须符合 DNS-1035），因此请勿使用 `11dog` 作为 Release 名，推荐 `dn11dog`，或设置 `fullnameOverride`。
> - 自行配置 Ingress 时：Web 路由需关闭响应缓冲以保证 SSE 实时流（如 Nginx `nginx.ingress.kubernetes.io/proxy-buffering: "off"`）；gRPC 路由需以 HTTP/2 明文（h2c）转发到 `grpc` 端口（如 Nginx `nginx.ingress.kubernetes.io/backend-protocol: "GRPC"`，Traefik `IngressRoute` 的 `scheme: h2c`）。
> - gRPC 为长连接流，注意放宽 Ingress 的读超时（如 Traefik v3 entrypoint 默认 `readTimeout=60s`，Nginx `proxy-read-timeout`），否则 Agent 会被周期性断开重连。

## 管理后台与前台隔离

- **前台测速页**（`/`）：仅展示面向普通用户的 Ping / TCPing / HTTP / Traceroute / DNS 测速界面及监测点地理位置，不暴露后台数据库、MQ 状态、节点硬件负载、IP 或接入配置。
- **管理后台**（`/admin`）：通过 `auth.admin_token`（或环境变量 `DOG_ADMIN_TOKEN`）鉴权，支持查看系统与 MQ 状态、在数据库中新增/编辑/启停节点、重置通信 Token、以及直接复制服务端托管的一键安装脚本。

## 可观测性与测试

- 健康检查：`GET /healthz`、`GET /readyz`
- Prometheus 指标：`GET /metrics`（含 `dog_nodes_online`、`dog_subtasks_overloaded_total`、`dog_node_queue_depth` 等）
- 运行全部 Go 单元测试与端到端集成测试：`go test -v ./...`
