# 11Dog — DN11 网络测速与拨测平台

**11Dog** 是面向 **DN11** 网络的分布式测速与拨测平台（ITDog 风格），支持多节点并发执行 ICMP Ping、TCPing、HTTP(S)、Traceroute 与 DNS 探测，并通过 SSE 实时流式展示结果。

- **原生 NATS JetStream + 去中心化 JWT/NKey 鉴权**：边缘节点持服务端签发的专属 `.creds` 直连 NATS（支持 `nats://`、`tls://`、`wss://`），Broker 原生强制隔离每个节点的 Subject 权限；控制面完全无状态，天然支持多副本水平扩展。
- **平台下发 Agent 自更新**：支持在管理后台单节点或批量下发更新指令，Agent 自动下载托管二进制、校验 SHA-256、等待探测排空后原子替换并自重启。
- **全局测试目标限制**：支持在后台配置全局允许/禁止规则（域名通配符与 IPv4/IPv6 CIDR），持久化保存在数据库、缓存在内存，并通过 NATS 实时同步至所有控制面副本。

---

## 控制端（Server）部署

### 方式一：Kubernetes（Helm，推荐）

Helm Chart 内置了 **NATS JetStream StatefulSet** 及 **JWT/NKey 自动初始化 Job**（`pre-install`/`pre-upgrade` 钩子通过 `natsio/nats-box` 幂等生成 Operator、SYS 与 11dog Account Secret，并自动注入 `DOG_NATS_ACCOUNT_SEED`）：

```bash
# 查看可配置参数
helm show values oci://ghcr.io/dn-11/charts/dn11dog > values.yaml

# 安装 / 升级（可附加 --version X.Y.Z 固定版本）
helm upgrade --install dn11dog oci://ghcr.io/dn-11/charts/dn11dog \
  --namespace 11dog --create-namespace \
  --set auth.adminToken="change-me-strong-admin-token" \
  --set nats.publicUrl="wss://nats.dn11.example"
```

- **控制面 Service**：`dn11dog:8080` 提供 Web 前台、`/admin` 管理后台、SSE 实时流及托管安装脚本/二进制下载；配置 Ingress 时请关闭响应缓冲以保证 SSE 实时推送（例如 Nginx Ingress 添加 `nginx.ingress.kubernetes.io/proxy-buffering: "off"`）。
- **NATS Service**：`dn11dog-nats` 暴露 `4222`（NATS TCP）与 `8443`（NATS WebSocket），可将 `8443` 通过普通 HTTPS Ingress 暴露为 `wss://` 供外部边缘 Agent 接入，或直接暴露 `4222` + TLS。
- **自定义 NATS 密钥 / 外部 NATS 集群**：可通过 `nats.auth.existingSecret` 复用已有 Secret（需包含 `account-seed`，内置 NATS 时还需 `nats-auth.conf`），或设置 `nats.enabled=false` + `nats.url` 连接外部 NATS 集群。
- **多副本高可用**：将 `db.driver` 切换为 `postgres` 并调大 `replicaCount` 即可。

未配置 Ingress 时，可先通过端口转发访问：

```bash
kubectl -n 11dog port-forward svc/dn11dog 8080:8080
```

### 方式二：Docker Compose（配合外部 NATS）

1. 先使用 `nsc` 初始化 NATS Operator / Account 配置（生成 `nats-auth.conf` 与 Account Seed `SA...`）：

```bash
docker run --rm -v "$(pwd)/nats-init:/out" natsio/nats-box:0.14.5 sh -eu -c '
  nsc add operator --name 11dog --sys
  nsc add account --name 11dog
  nsc edit account --name 11dog --js-mem-storage -1 --js-disk-storage -1 --js-streams -1 --js-consumer -1 --js-max-ack-pending -1
  nsc generate config --mem-resolver > /out/nats-auth.conf
  ACC_PUB=$(nsc describe account --name 11dog --json | jq -r ".sub")
  find /root/.local/share/nats/nsc/keys -name "${ACC_PUB}.nk" -exec cat {} + > /out/account.seed
'
```

2. 创建 `nats-server.conf`（与 `nats-auth.conf` 同目录）：

```hcl
port: 4222
http_port: 8222
jetstream {
  store_dir: "/data/jetstream"
}
include nats-auth.conf
```

3. 创建 `docker-compose.yml`：

```yaml
services:
  nats:
    image: nats:2.10-alpine
    restart: unless-stopped
    command: ["-c", "/etc/nats/nats-server.conf"]
    ports:
      - "4222:4222"
    volumes:
      - ./nats-init:/etc/nats:ro
      - nats-data:/data/jetstream

  11dog-server:
    image: ghcr.io/dn-11/11dog:latest
    container_name: 11dog-server
    restart: unless-stopped
    depends_on:
      - nats
    ports:
      - "8080:8080"
    environment:
      DOG_ADMIN_TOKEN: "change-me-strong-admin-token"
      DOG_DB_DSN: "/var/lib/11dog/11dog.db"
      DOG_NATS_URL: "nats://nats:4222"
      DOG_PUBLIC_NATS_URL: "nats://your-public-host:4222"
      DOG_NATS_ACCOUNT_SEED: "SA..." # 填入上一步生成的 account.seed 内容
    volumes:
      - 11dog-data:/var/lib/11dog

volumes:
  nats-data:
  11dog-data:
```

启动服务：

```bash
docker compose up -d
```

启动后访问：
- 前台测速页：`http://<IP>:8080`
- 节点管理后台：`http://<IP>:8080/admin`（使用 `DOG_ADMIN_TOKEN` 登录）

---

## 边缘节点（Agent）部署

支持 Linux `amd64` / `arm64`：

1. 登录管理后台 `/admin`，点击 **「+ 新增节点」** 填写节点名称、区域、网络组与 ASN（服务端自动签发该节点专属的 NATS `.creds` 凭据）；
2. 点击节点行的 **「部署脚本」**，复制一键安装命令并在边缘机上执行：

```bash
curl -fsSL "https://11dog.dn11.example/scripts/install.sh?token=dog_xxx&nats=wss://nats.dn11.example" | sudo bash
```

安装脚本会自动下载对应架构的 `11dog-agent` 至 `/opt/11dog/11dog-agent`（软链 `/usr/local/bin/11dog-agent`）、写入 `/etc/11dog/agent.creds` 与 `/etc/11dog/agent.yaml`，并注册为 `systemd` 服务（以非 root 用户 `11dog` 运行，授予 `CAP_NET_RAW` 支持原生 ICMP Ping / Traceroute，配置 `ReadWritePaths=/opt/11dog` 以支持平台下发自更新）。

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
