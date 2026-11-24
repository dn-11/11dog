# 11Dog — DN11 网络测速与拨测平台

**11Dog** 是面向 **DN11** 网络的分布式测速与拨测平台（ITDog 风格），支持多节点并发执行 ICMP Ping、TCPing、HTTP(S)、Traceroute 与 DNS 探测，并通过 SSE 实时流式展示结果。

- **单端口架构**：Server 仅监听 `8080`，同时提供 Web 前台、`/admin` 管理后台、SSE 实时流、Agent 一键安装脚本及反向 gRPC over WebSocket（`/api/v1/agent/ws`），开箱兼容 Nginx / Caddy / Traefik / K8s Ingress / CDN 等各类 HTTP(S) 网关。
- **反连边缘节点**：Agent 为纯 Go 静态二进制，主动通过 `ws://` / `wss://` 反连控制端，无需对边缘节点开放入站端口；节点身份与配置在后台统一管理。

---

## 控制端（Server）部署

### 方式一：Docker Compose

创建 `docker-compose.yml`：

```yaml
services:
  11dog-server:
    image: ghcr.io/dn-11/11dog:latest
    container_name: 11dog-server
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      DOG_ADMIN_TOKEN: "change-me-strong-admin-token"
      DOG_DB_DSN: "/var/lib/11dog/11dog.db"
      # 可选：若 Agent 反连域名与后台访问域名不同，可显式指定
      # DOG_PUBLIC_WS_ADDR: "wss://11dog.dn11.example/api/v1/agent/ws"
    volumes:
      - 11dog-data:/var/lib/11dog

volumes:
  11dog-data:
```

启动服务：

```bash
docker compose up -d
```

启动后访问：
- 前台测速页：`http://<IP>:8080`
- 节点管理后台：`http://<IP>:8080/admin`（使用 `DOG_ADMIN_TOKEN` 登录）

### 方式二：Kubernetes（Helm）

```bash
# 查看可配置参数
helm show values oci://ghcr.io/dn-11/charts/dn11dog > values.yaml

# 安装 / 升级（可附加 --version X.Y.Z 固定版本）
helm upgrade --install dn11dog oci://ghcr.io/dn-11/charts/dn11dog \
  --namespace 11dog --create-namespace \
  --set auth.adminToken="change-me-strong-admin-token"
```

- Chart 仅暴露单个 HTTP 端口 `8080`（默认 `ClusterIP`），请按集群环境自行配置普通 HTTP(S) Ingress / Gateway；配置时需允许 WebSocket Upgrade（`/api/v1/agent/ws`）并关闭响应缓冲以保证 SSE 实时推送（例如 Nginx Ingress 添加 `nginx.ingress.kubernetes.io/proxy-buffering: "off"`）。
- 未配置 Ingress 时，可先通过端口转发访问：`kubectl -n 11dog port-forward svc/dn11dog 8080:8080`。
- 如需多副本高可用，在 `values.yaml` 中将 `db.driver` 切换为 `postgres` 并调大 `replicaCount` 即可。

---

## 边缘节点（Agent）部署

支持 Linux `amd64` / `arm64`：

1. 登录管理后台 `/admin`，点击 **「+ 新增节点」** 填写节点名称、区域、网络组与 ASN；
2. 点击节点行的 **「部署脚本」**，复制一键安装命令并在边缘机上执行：

```bash
curl -fsSL "https://11dog.dn11.example/scripts/install.sh?token=dog_xxx" | sudo bash
```

安装脚本会自动下载对应架构的 `11dog-agent`、写入 `/etc/11dog/agent.yaml` 并注册为 `systemd` 服务（以非 root 用户 `11dog` 运行，授予 `CAP_NET_RAW` 权限以支持原生 ICMP Ping / Traceroute）。

常用运维命令：

```bash
# 查看运行日志
journalctl -u 11dog-agent -f

# 卸载 Agent
curl -fsSL "https://11dog.dn11.example/scripts/install.sh?uninstall=1" | sudo bash
```

---

## 监控与健康检查

- 健康检查：`GET /healthz`、`GET /readyz`
- Prometheus 指标：`GET /metrics`
