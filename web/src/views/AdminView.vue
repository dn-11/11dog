<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  createAdminNode,
  deleteAdminNode,
  disconnectAdminNode,
  fetchAdminNodes,
  fetchAdminOverview,
  fetchAdminTargetPolicy,
  rotateAdminNodeToken,
  triggerAdminBatchUpdate,
  triggerAdminNodeUpdate,
  updateAdminNode,
  updateAdminTargetPolicy
} from '@/api/client'
import type { AdminNodeInfo, AdminOverview, TargetPolicy } from '@/types/api'

const TOKEN_STORAGE_KEY = '11dog_admin_token'

const tokenInput = ref('')
const adminToken = ref('')
const authenticated = ref(false)
const authError = ref('')
const loading = ref(false)
const actionMsg = ref('')
const actionErr = ref('')

const overview = ref<AdminOverview | null>(null)
const nodes = ref<AdminNodeInfo[]>([])
const copiedTip = ref('')

// Create Node Modal state
const showCreateModal = ref(false)
const createForm = ref({
  name: '',
  region: '华东',
  group: 'DN11-BGP',
  asn: 4211110001,
  max_concurrency: 8,
  notes: ''
})

// Edit Node Modal state
const editingNode = ref<AdminNodeInfo | null>(null)
const editForm = ref({
  name: '',
  region: '',
  group: '',
  asn: 0,
  max_concurrency: 8,
  enabled: true,
  notes: ''
})

// Hosted Install Script Modal state
const deployNode = ref<AdminNodeInfo | null>(null)
const deployNATSAddr = ref('')

// Agent Self-Update Modal state
const showUpdateModal = ref(false)
const updateTargetNode = ref<AdminNodeInfo | null>(null) // null = batch all online nodes
const updateForm = ref({
  version: '',
  download_url: '',
  sha256: '',
  force: false
})
const updatingPush = ref(false)

// Global Target Restriction Policy state
const targetPolicy = ref<TargetPolicy>({
  enabled: false,
  allow_rules: [],
  deny_rules: []
})
const policyEnabled = ref(false)
const policyAllowText = ref('')
const policyDenyText = ref('')
const policyDirty = ref(false)
const policySaving = ref(false)

let refreshTimer: number | undefined

function defaultNATSAddr(): string {
  if (overview.value?.public_nats_url) {
    return overview.value.public_nats_url
  }
  const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${wsProto}//${window.location.host}`
}

const installCmd = computed(() => {
  if (!deployNode.value?.token) return ''
  const origin = window.location.origin
  const addr = deployNATSAddr.value.trim() || defaultNATSAddr()
  const maxConc = deployNode.value.max_concurrency || 8
  const args = [
    `--server "${origin}"`,
    `--token "${deployNode.value.token}"`,
    `--nats "${addr}"`,
    `--max-concurrency ${maxConc}`
  ]
  const dlBase = overview.value?.agent_download_base_url?.trim()
  if (dlBase) {
    args.push(`--download-base "${dlBase.replace(/\/+$/, '')}"`)
  }
  return `curl -fsSL "${origin}/scripts/install.sh" | sudo bash -s -- ${args.join(' ')}`
})

const uninstallCmd = computed(() => {
  return `curl -fsSL "${window.location.origin}/scripts/uninstall.sh" | sudo bash`
})

const minimalAgentYaml = computed(() => {
  if (!deployNode.value) return ''
  const addr = deployNATSAddr.value.trim() || defaultNATSAddr()
  return `nats_addr: "${addr}"
creds_file: "/etc/11dog/agent.creds"
pid_file: "/opt/11dog/11dog-agent.pid"
max_concurrency: ${deployNode.value.max_concurrency || 8}`
})

async function loadData(token: string): Promise<boolean> {
  loading.value = true
  authError.value = ''
  try {
    const [ov, nodeList, pol] = await Promise.all([
      fetchAdminOverview(token),
      fetchAdminNodes(token),
      fetchAdminTargetPolicy(token)
    ])
    overview.value = ov
    nodes.value = nodeList
    targetPolicy.value = pol
    if (!policyDirty.value) {
      policyEnabled.value = pol.enabled
      policyAllowText.value = (pol.allow_rules ?? []).join('\n')
      policyDenyText.value = (pol.deny_rules ?? []).join('\n')
    }
    authenticated.value = true
    adminToken.value = token
    sessionStorage.setItem(TOKEN_STORAGE_KEY, token)
    if (!deployNATSAddr.value) {
      deployNATSAddr.value = ov.public_nats_url || defaultNATSAddr()
    }
    return true
  } catch (err) {
    authError.value = err instanceof Error ? err.message : '认证失败'
    if (!authenticated.value) {
      sessionStorage.removeItem(TOKEN_STORAGE_KEY)
    }
    return false
  } finally {
    loading.value = false
  }
}

async function handleLogin() {
  const t = tokenInput.value.trim()
  if (!t) return
  await loadData(t)
}

function handleLogout() {
  sessionStorage.removeItem(TOKEN_STORAGE_KEY)
  adminToken.value = ''
  tokenInput.value = ''
  authenticated.value = false
  overview.value = null
  nodes.value = []
}

function openCreateModal() {
  actionErr.value = ''
  createForm.value = {
    name: '',
    region: '华东',
    group: 'DN11-BGP',
    asn: 4211110001,
    max_concurrency: 8,
    notes: ''
  }
  showCreateModal.value = true
}

async function submitCreateNode() {
  actionErr.value = ''
  actionMsg.value = ''
  try {
    const created = await createAdminNode(adminToken.value, {
      name: createForm.value.name.trim(),
      region: createForm.value.region.trim(),
      group: createForm.value.group.trim(),
      asn: Number(createForm.value.asn) || 0,
      max_concurrency: Number(createForm.value.max_concurrency) || 8,
      notes: createForm.value.notes.trim() || undefined
    })
    showCreateModal.value = false
    actionMsg.value = `已在数据库创建节点 ${created.name} (${created.id}) 并生成专属通信 Token`
    await loadData(adminToken.value)
    openDeployModal(created)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '创建节点失败'
  }
}

function openEditModal(n: AdminNodeInfo) {
  actionErr.value = ''
  editingNode.value = n
  editForm.value = {
    name: n.name,
    region: n.region,
    group: n.group,
    asn: n.asn,
    max_concurrency: n.max_concurrency,
    enabled: n.enabled,
    notes: n.notes ?? ''
  }
}

async function submitEditNode() {
  if (!editingNode.value) return
  actionErr.value = ''
  actionMsg.value = ''
  try {
    const updated = await updateAdminNode(adminToken.value, editingNode.value.id, {
      name: editForm.value.name.trim(),
      region: editForm.value.region.trim(),
      group: editForm.value.group.trim(),
      asn: Number(editForm.value.asn) || 0,
      max_concurrency: Number(editForm.value.max_concurrency) || 8,
      enabled: editForm.value.enabled,
      notes: editForm.value.notes.trim()
    })
    editingNode.value = null
    actionMsg.value = `已更新节点 ${updated.name} (${updated.id}) 数据库配置并同步至在线会话`
    await loadData(adminToken.value)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '更新节点失败'
  }
}

function openDeployModal(n: AdminNodeInfo) {
  deployNode.value = n
  if (overview.value?.public_nats_url) {
    deployNATSAddr.value = overview.value.public_nats_url
  } else if (!deployNATSAddr.value) {
    deployNATSAddr.value = defaultNATSAddr()
  }
}

async function handleRotateToken(n: AdminNodeInfo) {
  if (!window.confirm(`确定要为节点「${n.name} (${n.id})」重新签发 NATS .creds 凭据吗？`)) {
    return
  }
  actionErr.value = ''
  actionMsg.value = ''
  try {
    const updated = await rotateAdminNodeToken(adminToken.value, n.id)
    actionMsg.value = `节点 ${n.id} 已签发新的 NATS .creds 凭据`
    await loadData(adminToken.value)
    openDeployModal(updated)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '重新签发凭据失败'
  }
}

async function handleDisconnect(nodeID: string) {
  actionErr.value = ''
  actionMsg.value = ''
  try {
    await disconnectAdminNode(adminToken.value, nodeID)
    actionMsg.value = `已向节点 ${nodeID} 发送断开重连指令`
    setTimeout(() => {
      void loadData(adminToken.value)
    }, 800)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '操作失败'
  }
}

async function handleDelete(n: AdminNodeInfo) {
  if (!window.confirm(`确定从数据库永久删除节点「${n.name} (${n.id})」吗？此操作不可恢复。`)) {
    return
  }
  actionErr.value = ''
  actionMsg.value = ''
  try {
    await deleteAdminNode(adminToken.value, n.id)
    actionMsg.value = `节点 ${n.id} 已从数据库删除`
    await loadData(adminToken.value)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '删除失败'
  }
}

function openUpdateModal(n: AdminNodeInfo | null) {
  actionErr.value = ''
  updateTargetNode.value = n
  updateForm.value = {
    version: overview.value?.version || '',
    download_url: '',
    sha256: '',
    force: false
  }
  showUpdateModal.value = true
}

async function submitAgentUpdate() {
  actionErr.value = ''
  actionMsg.value = ''
  updatingPush.value = true
  try {
    const payload = {
      version: updateForm.value.version.trim() || undefined,
      download_url: updateForm.value.download_url.trim() || undefined,
      sha256: updateForm.value.sha256.trim() || undefined,
      force: updateForm.value.force
    }
    if (updateTargetNode.value) {
      const target = updateTargetNode.value
      const res = await triggerAdminNodeUpdate(adminToken.value, target.id, payload)
      actionMsg.value = `已向节点「${target.name} (${target.id})」下发更新指令：${res.message || '成功'}`
    } else {
      const resp = await triggerAdminBatchUpdate(adminToken.value, payload)
      const failed = resp.results.filter((r) => !r.sent)
      if (failed.length > 0) {
        actionMsg.value = `批量更新已下发：成功 ${resp.sent} / ${resp.total} 个节点（失败 ${failed.length} 个）`
      } else {
        actionMsg.value = `已向全部 ${resp.sent} 个在线节点下发更新指令`
      }
    }
    showUpdateModal.value = false
    setTimeout(() => {
      void loadData(adminToken.value)
    }, 600)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '下发更新失败'
  } finally {
    updatingPush.value = false
  }
}

function parseRuleLines(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith('#'))
}

async function saveTargetPolicy() {
  actionErr.value = ''
  actionMsg.value = ''
  policySaving.value = true
  try {
    const updated = await updateAdminTargetPolicy(adminToken.value, {
      enabled: policyEnabled.value,
      allow_rules: parseRuleLines(policyAllowText.value),
      deny_rules: parseRuleLines(policyDenyText.value)
    })
    targetPolicy.value = updated
    policyEnabled.value = updated.enabled
    policyAllowText.value = (updated.allow_rules ?? []).join('\n')
    policyDenyText.value = (updated.deny_rules ?? []).join('\n')
    policyDirty.value = false
    actionMsg.value = updated.enabled
      ? `全局目标限制已保存至数据库并刷新内存缓存（允许 ${updated.allow_rules.length} 条，禁止 ${updated.deny_rules.length} 条）`
      : '全局目标限制已关闭并保存至数据库'
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '保存目标限制策略失败'
  } finally {
    policySaving.value = false
  }
}

function updateStateBadgeClass(state?: string): string {
  switch (state) {
    case 'failed':
      return 'badge-red'
    case 'up_to_date':
      return 'badge-green'
    case 'restarting':
      return 'badge-purple'
    default:
      return 'badge-amber'
  }
}

function updateStateLabel(state?: string): string {
  switch (state) {
    case 'pending':
      return '等待更新'
    case 'downloading':
      return '下载中'
    case 'verifying':
      return '校验中'
    case 'applying':
      return '替换中'
    case 'restarting':
      return '重启中'
    case 'up_to_date':
      return '已是最新'
    case 'failed':
      return '更新失败'
    default:
      return state || ''
  }
}

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text)
    copiedTip.value = `${label} 已复制`
    setTimeout(() => {
      copiedTip.value = ''
    }, 2000)
  } catch {
    copiedTip.value = '复制失败，请手动选中'
  }
}

function formatTime(ms: number): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleTimeString()
}

onMounted(() => {
  const saved = sessionStorage.getItem(TOKEN_STORAGE_KEY)
  if (saved) {
    void loadData(saved)
  }
  refreshTimer = window.setInterval(() => {
    if (authenticated.value && adminToken.value) {
      void loadData(adminToken.value)
    }
  }, 8000)
})

onUnmounted(() => {
  if (refreshTimer !== undefined) {
    window.clearInterval(refreshTimer)
  }
})
</script>

<template>
  <div>
    <!-- Login Screen -->
    <div v-if="!authenticated" class="login-wrap">
      <div class="card login-card">
        <div class="card-header">
          <span>11Dog 运维管理后台认证</span>
        </div>
        <div class="card-body">
          <p class="login-desc">
            所有节点身份与专属通信 Token 均在服务端数据库统一管理。请输入
            <code class="mono">admin_token</code> 登录管理后台。
          </p>
          <form class="login-form" @submit.prevent="handleLogin">
            <input
              v-model="tokenInput"
              type="password"
              class="login-input mono"
              placeholder="输入 Admin Token (DOG_ADMIN_TOKEN)"
              autocomplete="current-password"
            />
            <button type="submit" class="btn btn-primary" :disabled="!tokenInput.trim() || loading">
              {{ loading ? '验证中...' : '进入后台' }}
            </button>
          </form>
          <div v-if="authError" class="error-banner" style="margin-top: 12px; margin-bottom: 0">
            {{ authError }}
          </div>
        </div>
      </div>
    </div>

    <!-- Authenticated Admin Dashboard -->
    <template v-else>
      <div class="admin-topbar">
        <div class="admin-title-group">
          <h2 class="admin-title">控制面与节点数据库管理</h2>
          <span v-if="overview" class="badge badge-blue mono">Ver: {{ overview.version }}</span>
          <span v-if="overview" class="badge badge-green mono">DB: {{ overview.db_driver.toUpperCase() }}</span>
          <span v-if="overview" class="badge badge-purple mono">
            NATS: {{ overview.public_nats_url || overview.nats_url }} ({{ overview.max_queue_per_node }}/节点)
          </span>
          <span v-if="overview?.nats_account_pub" class="badge badge-slate mono">
            Acc: {{ overview.nats_account_pub.slice(0, 12) }}…
          </span>
        </div>
        <div class="header-actions">
          <button type="button" class="btn btn-success btn-sm" @click="openCreateModal">
            + 新增节点
          </button>
          <button type="button" class="btn btn-primary btn-sm" :disabled="loading" @click="loadData(adminToken)">
            {{ loading ? '刷新中...' : '刷新状态' }}
          </button>
          <button type="button" class="btn btn-outline btn-sm" @click="handleLogout">退出登录</button>
        </div>
      </div>

      <div v-if="actionMsg" class="info-banner">{{ actionMsg }}</div>
      <div v-if="actionErr" class="error-banner">{{ actionErr }}</div>

      <div v-if="overview" class="nodes-kpi">
        <div class="card kpi-card">
          <div class="kpi-title">在线 / 离线节点 (数据库共 {{ overview.total_nodes }})</div>
          <div class="kpi-num mono">
            <span style="color: #16a34a">{{ overview.online_nodes }}</span>
            <span class="text-muted"> / {{ overview.offline_nodes }}</span>
          </div>
        </div>
        <div class="card kpi-card">
          <div class="kpi-title">全网并发运行 / 容量</div>
          <div class="kpi-num mono" style="color: #1677ff">
            {{ overview.active_tasks }} / {{ overview.total_capacity }}
          </div>
        </div>
        <div class="card kpi-card">
          <div class="kpi-title">MQ 缓冲积压总量</div>
          <div class="kpi-num mono" style="color: #7c3aed">{{ overview.total_queue_depth }}</div>
        </div>
        <div class="card kpi-card">
          <div class="kpi-title">任务总数 / 完成 / 过载 / 过期</div>
          <div class="kpi-num mono text-stats">
            {{ overview.tasks_created_total }} /
            <span style="color: #16a34a">{{ overview.subtasks_completed_total }}</span> /
            <span style="color: #dc2626">{{ overview.subtasks_overloaded_total }}</span> /
            <span style="color: #d97706">{{ overview.subtasks_expired_total }}</span>
          </div>
        </div>
      </div>

      <!-- Hosted Binary Status Banner -->
      <div v-if="overview" class="card">
        <div class="card-body hosted-bar">
          <div>
            <strong>托管安装脚本与二进制分发：</strong>
            <span class="mono text-sm">GET /scripts/install.sh</span>
            <span class="text-muted" style="margin-left: 8px">（服务端已托管静态脚本与 Agent 下载）</span>
          </div>
          <div class="hosted-bins">
            <span
              v-for="bin in overview.hosted_binaries"
              :key="bin"
              class="badge badge-green mono"
            >
              ✓ {{ bin }}
            </span>
            <span v-if="!overview.hosted_binaries?.length" class="badge badge-amber">
              未检测到托管的 Agent 二进制文件（请构建放入 agent_bin_dir）
            </span>
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="overview.online_nodes === 0"
              @click="openUpdateModal(null)"
            >
              批量下发 Agent 更新
            </button>
          </div>
        </div>
      </div>

      <!-- Global Target Restriction Policy Card -->
      <div class="card">
        <div class="card-header">
          <div class="admin-title-group">
            <span>全局测试目标限制（域名通配符 / CIDR）</span>
            <span class="badge" :class="targetPolicy.enabled ? 'badge-green' : 'badge-slate'">
              {{ targetPolicy.enabled ? '已启用（内存缓存生效）' : '未启用' }}
            </span>
          </div>
          <div class="header-actions">
            <span v-if="targetPolicy.updated_at_ms" class="mono text-muted">
              上次更新: {{ formatTime(targetPolicy.updated_at_ms) }}
            </span>
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="policySaving"
              @click="saveTargetPolicy"
            >
              {{ policySaving ? '保存中...' : '保存策略' }}
            </button>
          </div>
        </div>
        <div class="card-body">
          <label class="field-check" style="margin-bottom: 12px">
            <input
              v-model="policyEnabled"
              type="checkbox"
              @change="policyDirty = true"
            />
            <span>启用全局测试目标限制（持久化保存在数据库并实时缓存于内存；禁止列表优先于允许列表）</span>
          </label>
          <div class="form-grid">
            <label class="field-item">
              <span>允许列表（Allow Rules，每行一条；留空表示允许除禁止列表外的所有目标）</span>
              <textarea
                v-model="policyAllowText"
                rows="4"
                class="policy-textarea mono"
                placeholder="例如：&#10;172.16.0.0/16&#10;fd00::/48&#10;*.dn11.org&#10;example.com"
                @input="policyDirty = true"
              ></textarea>
            </label>
            <label class="field-item">
              <span>禁止列表（Deny Rules，每行一条；优先拦截匹配的 IP / CIDR / 域名通配符）</span>
              <textarea
                v-model="policyDenyText"
                rows="4"
                class="policy-textarea mono"
                placeholder="例如：&#10;127.0.0.0/8&#10;169.254.0.0/16&#10;*.internal.local"
                @input="policyDirty = true"
              ></textarea>
            </label>
          </div>
        </div>
      </div>

      <!-- Node Telemetry & DB CRUD Table -->
      <div class="card">
        <div class="card-header">
          <span>节点数据库与实时遥测（服务端权威身份）</span>
          <span class="mono text-muted">每 8 秒自动刷新 · 共 {{ nodes.length }} 个节点</span>
        </div>
        <div class="data-table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>状态</th>
                <th>节点名称 / ID</th>
                <th>区域 / 网络组</th>
                <th>DN11 ASN</th>
                <th>接入 IP</th>
                <th>并发负载</th>
                <th>MQ 缓冲</th>
                <th>CPU / 内存</th>
                <th>心跳</th>
                <th>管理操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="n in nodes" :key="n.id" :class="{ 'row-disabled': !n.enabled }">
                <td>
                  <div class="status-col">
                    <span class="badge" :class="n.status === 'online' ? 'badge-green' : 'badge-slate'">
                      <span :class="n.status === 'online' ? 'dot-online' : 'dot-offline'"></span>
                      {{ n.status === 'online' ? '在线' : '离线' }}
                    </span>
                    <span v-if="!n.enabled" class="badge badge-red">已禁用</span>
                  </div>
                </td>
                <td>
                  <div style="font-weight: 600">{{ n.name }}</div>
                  <div class="mono text-muted">{{ n.id }}</div>
                  <div v-if="n.notes" class="text-muted">{{ n.notes }}</div>
                </td>
                <td>
                  <span class="badge badge-blue">{{ n.region }}</span>
                  <span class="mono text-muted" style="margin-left: 6px">{{ n.group }}</span>
                </td>
                <td class="mono">{{ n.asn ? `AS${n.asn}` : '-' }}</td>
                <td class="mono text-sm">
                  <div v-if="n.ipv4">{{ n.ipv4 }}</div>
                  <div v-if="n.ipv6" class="text-muted">{{ n.ipv6 }}</div>
                  <span v-if="!n.ipv4 && !n.ipv6" class="text-muted">-</span>
                </td>
                <td>
                  <div class="mono text-sm">{{ n.active_tasks }} / {{ n.max_concurrency }}</div>
                  <div class="mini-bar">
                    <div
                      class="mini-fill"
                      :style="{
                        width: `${Math.min(100, (n.active_tasks / Math.max(1, n.max_concurrency)) * 100)}%`,
                        background: n.active_tasks >= n.max_concurrency ? '#ef4444' : '#1677ff'
                      }"
                    ></div>
                  </div>
                </td>
                <td>
                  <div class="mono text-sm">
                    {{ n.queue_depth }} / {{ overview?.max_queue_per_node ?? 20 }}
                  </div>
                  <div class="mini-bar">
                    <div
                      class="mini-fill"
                      :style="{
                        width: `${Math.min(100, (n.queue_depth / Math.max(1, overview?.max_queue_per_node ?? 20)) * 100)}%`,
                        background: '#7c3aed'
                      }"
                    ></div>
                  </div>
                </td>
                <td class="mono text-sm">{{ n.cpu_percent.toFixed(1) }}% / {{ n.mem_percent.toFixed(1) }}%</td>
                <td class="mono text-sm">
                  <div>
                    {{ n.version || '-' }}
                    <span v-if="n.os || n.arch" class="text-muted">
                      ({{ n.os || '?' }}/{{ n.arch || '?' }})
                    </span>
                  </div>
                  <div v-if="n.update_status" style="margin-top: 2px">
                    <span
                      class="badge"
                      :class="updateStateBadgeClass(n.update_status.state)"
                      :title="n.update_status.message || ''"
                    >
                      {{ updateStateLabel(n.update_status.state) }}
                    </span>
                  </div>
                  <div class="text-muted">{{ formatTime(n.last_seen_unix_ms) }}</div>
                </td>
                <td>
                  <div class="row-actions">
                    <button type="button" class="btn btn-primary btn-sm" @click="openDeployModal(n)">
                      部署脚本
                    </button>
                    <button
                      v-if="n.status === 'online'"
                      type="button"
                      class="btn btn-success btn-sm"
                      @click="openUpdateModal(n)"
                    >
                      更新
                    </button>
                    <button type="button" class="btn btn-outline btn-sm" @click="openEditModal(n)">
                      编辑
                    </button>
                    <button type="button" class="btn btn-outline btn-sm" @click="handleRotateToken(n)">
                      重签Creds
                    </button>
                    <button
                      v-if="n.status === 'online'"
                      type="button"
                      class="btn btn-outline btn-sm"
                      @click="handleDisconnect(n.id)"
                    >
                      重连
                    </button>
                    <button type="button" class="btn btn-danger btn-sm" @click="handleDelete(n)">
                      删除
                    </button>
                  </div>
                </td>
              </tr>
              <tr v-if="nodes.length === 0">
                <td colspan="10" class="empty-nodes">
                  数据库中尚无节点记录，请点击右上角「+ 新增节点」创建节点并获取专属 systemd 安装命令。
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Create Node Modal -->
      <div v-if="showCreateModal" class="modal-backdrop" @click.self="showCreateModal = false">
        <div class="card modal-card">
          <div class="card-header">
            <span>新增监测节点（写入数据库并签发专属 Token）</span>
            <button type="button" class="btn btn-outline btn-sm" @click="showCreateModal = false">关闭</button>
          </div>
          <form class="card-body modal-form" @submit.prevent="submitCreateNode">
            <div class="form-grid">
              <label class="field-item">
                <span>节点名称 *</span>
                <input v-model="createForm.name" type="text" required placeholder="例如：香港 BGP 01" />
              </label>
              <label class="field-item">
                <span>区域标签 *</span>
                <input v-model="createForm.region" type="text" required placeholder="例如：香港 / 华东" />
              </label>
              <label class="field-item">
                <span>网络组 / 运营商 *</span>
                <input v-model="createForm.group" type="text" required placeholder="例如：DN11-BGP" />
              </label>
              <label class="field-item">
                <span>DN11 ASN</span>
                <input v-model.number="createForm.asn" type="number" class="mono" />
              </label>
              <label class="field-item">
                <span>最大并发数</span>
                <input v-model.number="createForm.max_concurrency" type="number" min="1" max="128" class="mono" />
              </label>
              <label class="field-item full-span">
                <span>备注</span>
                <input v-model="createForm.notes" type="text" placeholder="可选节点说明" />
              </label>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-outline btn-sm" @click="showCreateModal = false">取消</button>
              <button type="submit" class="btn btn-primary btn-sm" :disabled="!createForm.name.trim()">
                创建并生成部署脚本
              </button>
            </div>
          </form>
        </div>
      </div>

      <!-- Edit Node Modal -->
      <div v-if="editingNode" class="modal-backdrop" @click.self="editingNode = null">
        <div class="card modal-card">
          <div class="card-header">
            <span>编辑节点信息 ({{ editingNode.id }})</span>
            <button type="button" class="btn btn-outline btn-sm" @click="editingNode = null">关闭</button>
          </div>
          <form class="card-body modal-form" @submit.prevent="submitEditNode">
            <div class="form-grid">
              <label class="field-item">
                <span>节点名称 *</span>
                <input v-model="editForm.name" type="text" required />
              </label>
              <label class="field-item">
                <span>区域标签 *</span>
                <input v-model="editForm.region" type="text" required />
              </label>
              <label class="field-item">
                <span>网络组 *</span>
                <input v-model="editForm.group" type="text" required />
              </label>
              <label class="field-item">
                <span>DN11 ASN</span>
                <input v-model.number="editForm.asn" type="number" class="mono" />
              </label>
              <label class="field-item">
                <span>最大并发数</span>
                <input v-model.number="editForm.max_concurrency" type="number" min="1" max="128" class="mono" />
              </label>
              <label class="field-check">
                <input v-model="editForm.enabled" type="checkbox" />
                <span>启用该节点（取消勾选将立即下线并拒绝重连）</span>
              </label>
              <label class="field-item full-span">
                <span>备注</span>
                <input v-model="editForm.notes" type="text" />
              </label>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-outline btn-sm" @click="editingNode = null">取消</button>
              <button type="submit" class="btn btn-primary btn-sm">保存修改</button>
            </div>
          </form>
        </div>
      </div>

      <!-- Deploy Script Modal -->
      <div v-if="deployNode" class="modal-backdrop" @click.self="deployNode = null">
        <div class="card modal-card wide">
          <div class="card-header">
            <span>部署节点「{{ deployNode.name }}」({{ deployNode.id }})</span>
            <div class="header-actions">
              <span v-if="copiedTip" class="badge badge-green">{{ copiedTip }}</span>
              <button type="button" class="btn btn-outline btn-sm" @click="deployNode = null">关闭</button>
            </div>
          </div>
          <div class="card-body">
            <p class="deploy-hint">
              Agent 使用服务端签发的 NATS JWT/NKey 凭据（<code class="mono">.creds</code>）直连 NATS，Broker 原生强制隔离每个节点的 Subject 权限。
            </p>

            <div class="form-grid" style="margin-bottom: 12px">
              <label class="field-item full-span">
                <span>NATS WebSocket 地址（ws:// 或 wss://）</span>
                <input v-model="deployNATSAddr" type="text" class="mono" />
              </label>
            </div>

            <div class="code-section">
              <div class="code-head">
                <span>1. 一键安装命令（托管在 11Dog 服务端，自动写入 .creds 与 systemd）</span>
                <button type="button" class="btn btn-primary btn-sm" @click="copyText('安装命令', installCmd)">
                  复制安装命令
                </button>
              </div>
              <pre class="mono guide-pre">{{ installCmd }}</pre>
            </div>

            <div class="code-section">
              <div class="code-head">
                <span>2. 节点专属 NATS .creds 凭据 (/etc/11dog/agent.creds)</span>
                <button type="button" class="btn btn-outline btn-sm" @click="copyText('NATS .creds', deployNode.creds || '')">
                  复制 .creds
                </button>
              </div>
              <pre class="mono guide-pre">{{ deployNode.creds || '未签发，请点击重签Creds' }}</pre>
            </div>

            <div class="code-section">
              <div class="code-head">
                <span>3. 手动配置文件参考 (/etc/11dog/agent.yaml)</span>
                <button type="button" class="btn btn-outline btn-sm" @click="copyText('YAML 配置', minimalAgentYaml)">
                  复制配置
                </button>
              </div>
              <pre class="mono guide-pre">{{ minimalAgentYaml }}</pre>
            </div>

            <div class="code-section" style="margin-bottom: 0">
              <div class="code-head">
                <span>4. 卸载命令</span>
                <button type="button" class="btn btn-outline btn-sm" @click="copyText('卸载命令', uninstallCmd)">
                  复制卸载命令
                </button>
              </div>
              <pre class="mono guide-pre">{{ uninstallCmd }}</pre>
            </div>
          </div>
        </div>
      </div>

      <!-- Agent Self-Update Modal -->
      <div v-if="showUpdateModal" class="modal-backdrop" @click.self="showUpdateModal = false">
        <div class="card modal-card">
          <div class="card-header">
            <span>
              {{
                updateTargetNode
                  ? `下发 Agent 更新：${updateTargetNode.name} (${updateTargetNode.id})`
                  : '批量下发 Agent 更新（全部在线节点）'
              }}
            </span>
            <button type="button" class="btn btn-outline btn-sm" @click="showUpdateModal = false">关闭</button>
          </div>
          <form class="card-body modal-form" @submit.prevent="submitAgentUpdate">
            <p class="deploy-hint">
              留空下载地址时，服务端将自动根据每个节点的 OS/Arch 匹配托管目录（<code class="mono">/downloads/11dog-agent-&lt;os&gt;-&lt;arch&gt;</code>）并自动计算 SHA-256 校验值下发。
            </p>
            <div class="form-grid">
              <label class="field-item full-span">
                <span>目标版本号（留空默认服务端版本 {{ overview?.version || '-' }}）</span>
                <input v-model="updateForm.version" type="text" class="mono" placeholder="例如：1.1.0" />
              </label>
              <label class="field-item full-span">
                <span>自定义下载地址（可选，留空自动使用托管二进制）</span>
                <input
                  v-model="updateForm.download_url"
                  type="text"
                  class="mono"
                  placeholder="例如：https://example.com/11dog-agent-linux-amd64"
                />
              </label>
              <label class="field-item full-span">
                <span>SHA-256 校验值（可选，托管二进制自动计算）</span>
                <input
                  v-model="updateForm.sha256"
                  type="text"
                  class="mono"
                  placeholder="64 位十六进制 SHA-256"
                />
              </label>
              <label class="field-check full-span">
                <input v-model="updateForm.force" type="checkbox" />
                <span>强制更新（即使 Agent 当前版本与目标版本一致也重新下载替换并重启）</span>
              </label>
            </div>
            <div class="modal-footer">
              <button type="button" class="btn btn-outline btn-sm" @click="showUpdateModal = false">取消</button>
              <button type="submit" class="btn btn-primary btn-sm" :disabled="updatingPush">
                {{ updatingPush ? '下发中...' : '确认下发更新' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.login-wrap {
  display: flex;
  justify-content: center;
  padding-top: 48px;
}

.login-card {
  width: 100%;
  max-width: 460px;
}

.login-desc {
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--text-secondary);
}

.login-form {
  display: flex;
  gap: 8px;
}

.login-input {
  flex: 1;
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  padding: 0 12px;
  height: 46px;
  font-size: 14px;
  outline: none;
}

.login-input:focus {
  border-color: var(--brand-blue);
}

.admin-topbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 14px;
}

.admin-title-group {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.admin-title {
  margin: 0;
  font-size: 18px;
  font-weight: 700;
}

.info-banner {
  background: #eff6ff;
  border: 1px solid #bfdbfe;
  color: #1d4ed8;
  padding: 10px 14px;
  border-radius: 6px;
  margin-bottom: 14px;
  font-size: 13px;
}

.nodes-kpi {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}

.kpi-card {
  padding: 14px 18px;
}

.kpi-title {
  font-size: 12px;
  color: var(--text-secondary);
}

.kpi-num {
  font-size: 22px;
  font-weight: 800;
  margin-top: 4px;
}

.text-stats {
  font-size: 16px;
}

.hosted-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 12px 18px;
}

.hosted-bins {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.status-col {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: flex-start;
}

.row-disabled {
  opacity: 0.65;
  background: #fef2f2;
}

.row-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.text-muted {
  color: var(--text-muted);
  font-size: 11px;
}

.text-sm {
  font-size: 12px;
}

.mini-bar {
  width: 84px;
  height: 5px;
  background: #e2e8f0;
  border-radius: 999px;
  overflow: hidden;
  margin-top: 4px;
}

.mini-fill {
  height: 100%;
  transition: width 0.2s ease;
}

.empty-nodes {
  text-align: center;
  padding: 36px;
  color: var(--text-muted);
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.52);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  z-index: 60;
}

.modal-card {
  width: 100%;
  max-width: 560px;
  max-height: 90vh;
  overflow-y: auto;
  margin-bottom: 0;
}

.modal-card.wide {
  max-width: 740px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
}

.field-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  color: var(--text-secondary);
}

.field-item.full-span {
  grid-column: 1 / -1;
}

.field-item input,
.policy-textarea {
  border: 1px solid var(--border-strong);
  border-radius: 5px;
  padding: 7px 10px;
  font-size: 13px;
  color: var(--text-primary);
}

.policy-textarea {
  resize: vertical;
  line-height: 1.5;
  font-size: 12px;
}

.field-check.full-span {
  grid-column: 1 / -1;
}

.field-check {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-secondary);
}

.modal-footer {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.deploy-hint {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--text-secondary);
  background: #f0f9ff;
  border: 1px solid #bae6fd;
  padding: 8px 12px;
  border-radius: 6px;
}

.code-section {
  margin-bottom: 12px;
}

.code-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 5px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
}

.guide-pre {
  background: #0f172a;
  color: #e2e8f0;
  padding: 10px 12px;
  border-radius: 6px;
  margin: 0;
  overflow-x: auto;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}

@media (max-width: 960px) {
  .nodes-kpi {
    grid-template-columns: repeat(2, 1fr);
  }
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
