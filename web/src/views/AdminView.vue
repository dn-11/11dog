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

type AdminSection = 'overview' | 'nodes' | 'updates' | 'policy'
type DeployTab = 'systemd' | 'docker' | 'creds' | 'yaml' | 'uninstall'

const TOKEN_STORAGE_KEY = '11dog_admin_token'

const tokenInput = ref('')
const adminToken = ref('')
const authenticated = ref(false)
const authError = ref('')
const loading = ref(false)
const actionMsg = ref('')
const actionErr = ref('')

const activeSection = ref<AdminSection>('overview')
const overview = ref<AdminOverview | null>(null)
const nodes = ref<AdminNodeInfo[]>([])
const copiedTip = ref('')

// Nodes view filters
const nodeKeyword = ref('')
const nodeStatusFilter = ref<'all' | 'online' | 'offline' | 'disabled'>('all')

// Updates view selection
const selectedUpdateNodeIDs = ref<string[]>([])

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
const deployTab = ref<DeployTab>('systemd')

// Agent Self-Update Modal state
const showUpdateModal = ref(false)
const updateTargetNode = ref<AdminNodeInfo | null>(null) // null = batch selected or all online nodes
const updateTargetNodeIDs = ref<string[]>([])
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

const onlineNodes = computed(() => nodes.value.filter((n) => n.status === 'online'))
const offlineNodes = computed(() => nodes.value.filter((n) => n.status !== 'online'))
const disabledNodes = computed(() => nodes.value.filter((n) => !n.enabled))

const outdatedOnlineNodes = computed(() => {
  const srvVer = overview.value?.version?.trim()
  if (!srvVer || srvVer === 'dev') return []
  return onlineNodes.value.filter((n) => n.version && n.version !== srvVer)
})

const failedUpdateNodes = computed(() =>
  nodes.value.filter((n) => n.update_status?.state === 'failed')
)

const filteredNodes = computed(() => {
  const kw = nodeKeyword.value.trim().toLowerCase()
  return nodes.value.filter((n) => {
    if (nodeStatusFilter.value === 'online' && n.status !== 'online') return false
    if (nodeStatusFilter.value === 'offline' && n.status === 'online') return false
    if (nodeStatusFilter.value === 'disabled' && n.enabled) return false
    if (kw) {
      const hay = `${n.name} ${n.id} ${n.region} ${n.group} ${n.ipv4 ?? ''} AS${n.asn} ${n.notes ?? ''}`.toLowerCase()
      if (!hay.includes(kw)) return false
    }
    return true
  })
})

const allowRuleCount = computed(() => parseRuleLines(policyAllowText.value).length)
const denyRuleCount = computed(() => parseRuleLines(policyDenyText.value).length)

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

const dockerComposeAgentYaml = computed(() => {
  if (!deployNode.value) return ''
  const addr = deployNATSAddr.value.trim() || defaultNATSAddr()
  const maxConc = deployNode.value.max_concurrency || 8
  return `services:
  11dog-agent:
    image: ghcr.io/dn-11/11dog-agent:latest
    container_name: 11dog-agent
    restart: unless-stopped
    network_mode: host
    cap_drop:
      - ALL
    cap_add:
      - NET_RAW
    environment:
      DOG_NATS_ADDR: "${addr}"
      DOG_NATS_CREDS_FILE: "/etc/11dog/agent.creds"
      DOG_MAX_CONCURRENCY: "${maxConc}"
    volumes:
      - ./agent.creds:/etc/11dog/agent.creds:ro
      - /etc/resolv.conf:/etc/resolv.conf:ro
      - /etc/hosts:/etc/hosts:ro`
})

function clearBanners() {
  actionMsg.value = ''
  actionErr.value = ''
}

function switchSection(section: AdminSection) {
  activeSection.value = section
  clearBanners()
}

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
    const onlineSet = new Set(nodeList.filter((n) => n.status === 'online').map((n) => n.id))
    selectedUpdateNodeIDs.value = selectedUpdateNodeIDs.value.filter((id) => onlineSet.has(id))

    const allowRules = pol.allow_rules ?? []
    const denyRules = pol.deny_rules ?? []
    targetPolicy.value = {
      ...pol,
      allow_rules: allowRules,
      deny_rules: denyRules
    }
    if (!policyDirty.value) {
      policyEnabled.value = pol.enabled
      policyAllowText.value = allowRules.join('\n')
      policyDenyText.value = denyRules.join('\n')
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
  clearBanners()
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
    activeSection.value = 'nodes'
    actionMsg.value = `已创建节点「${created.name}」并签发专属 NATS 凭据，请按向导完成部署`
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
  clearBanners()
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
    actionMsg.value = `已更新节点「${updated.name}」配置`
    await loadData(adminToken.value)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '更新节点失败'
  }
}

function openDeployModal(n: AdminNodeInfo) {
  deployNode.value = n
  deployTab.value = 'systemd'
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
  clearBanners()
  try {
    const updated = await rotateAdminNodeToken(adminToken.value, n.id)
    actionMsg.value = `节点「${n.name}」已签发新的 NATS .creds 凭据`
    await loadData(adminToken.value)
    openDeployModal(updated)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '重新签发凭据失败'
  }
}

async function handleDisconnect(n: AdminNodeInfo) {
  clearBanners()
  try {
    await disconnectAdminNode(adminToken.value, n.id)
    actionMsg.value = `已向节点「${n.name}」发送断开重连指令`
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
  clearBanners()
  try {
    await deleteAdminNode(adminToken.value, n.id)
    actionMsg.value = `节点「${n.name}」已从数据库删除`
    await loadData(adminToken.value)
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '删除失败'
  }
}

function toggleUpdateSelect(nodeID: string) {
  const set = new Set(selectedUpdateNodeIDs.value)
  if (set.has(nodeID)) set.delete(nodeID)
  else set.add(nodeID)
  selectedUpdateNodeIDs.value = Array.from(set)
}

function selectAllOnlineForUpdate() {
  selectedUpdateNodeIDs.value = onlineNodes.value.map((n) => n.id)
}

function selectOutdatedForUpdate() {
  selectedUpdateNodeIDs.value = outdatedOnlineNodes.value.map((n) => n.id)
}

function openUpdateModal(n: AdminNodeInfo | null, nodeIDs: string[] = []) {
  actionErr.value = ''
  updateTargetNode.value = n
  updateTargetNodeIDs.value = n ? [] : [...nodeIDs]
  updateForm.value = {
    version: overview.value?.version && overview.value.version !== 'dev' ? overview.value.version : '',
    download_url: '',
    sha256: '',
    force: false
  }
  showUpdateModal.value = true
}

async function submitAgentUpdate() {
  clearBanners()
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
      actionMsg.value = `已向节点「${target.name}」下发更新指令：${res.message || '成功'}`
    } else {
      const resp = await triggerAdminBatchUpdate(adminToken.value, {
        ...payload,
        node_ids: updateTargetNodeIDs.value.length > 0 ? updateTargetNodeIDs.value : undefined
      })
      const failed = resp.results.filter((r) => !r.sent)
      if (failed.length > 0) {
        actionMsg.value = `批量更新已下发：成功 ${resp.sent} / ${resp.total} 个节点（失败 ${failed.length} 个）`
      } else {
        actionMsg.value = `已向 ${resp.sent} 个在线节点下发更新指令`
      }
      selectedUpdateNodeIDs.value = []
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
  clearBanners()
  policySaving.value = true
  try {
    const updated = await updateAdminTargetPolicy(adminToken.value, {
      enabled: policyEnabled.value,
      allow_rules: parseRuleLines(policyAllowText.value),
      deny_rules: parseRuleLines(policyDenyText.value)
    })
    const allowRules = updated.allow_rules ?? []
    const denyRules = updated.deny_rules ?? []
    targetPolicy.value = {
      ...updated,
      allow_rules: allowRules,
      deny_rules: denyRules
    }
    policyEnabled.value = updated.enabled
    policyAllowText.value = allowRules.join('\n')
    policyDenyText.value = denyRules.join('\n')
    policyDirty.value = false
    actionMsg.value = updated.enabled
      ? `全局目标限制已保存并实时生效（允许 ${allowRules.length} 条，禁止 ${denyRules.length} 条）`
      : '全局目标限制已关闭并保存'
  } catch (err) {
    actionErr.value = err instanceof Error ? err.message : '保存目标限制策略失败'
  } finally {
    policySaving.value = false
  }
}

function resetPolicyDraft() {
  policyEnabled.value = targetPolicy.value.enabled
  policyAllowText.value = (targetPolicy.value.allow_rules ?? []).join('\n')
  policyDenyText.value = (targetPolicy.value.deny_rules ?? []).join('\n')
  policyDirty.value = false
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

function isNodeOutdated(n: AdminNodeInfo): boolean {
  const srvVer = overview.value?.version?.trim()
  return Boolean(srvVer && srvVer !== 'dev' && n.version && n.version !== srvVer)
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
            管理节点身份凭据、Agent 自更新分发及全局测试目标访问策略。请输入
            <code class="mono">admin_token</code> 登录。
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

    <!-- Authenticated Admin Workspace -->
    <div v-else class="admin-shell">
      <!-- Left Sidebar Navigation -->
      <aside class="admin-sidebar card">
        <div class="sidebar-brand">
          <div class="sidebar-title">11Dog 控制台</div>
          <div v-if="overview" class="sidebar-ver mono">Server {{ overview.version }}</div>
        </div>

        <nav class="sidebar-nav">
          <button
            type="button"
            class="side-item"
            :class="{ active: activeSection === 'overview' }"
            @click="switchSection('overview')"
          >
            <span class="side-label">运行概览</span>
            <span v-if="overview" class="side-pill mono">{{ overview.online_nodes }}/{{ overview.total_nodes }}</span>
          </button>

          <button
            type="button"
            class="side-item"
            :class="{ active: activeSection === 'nodes' }"
            @click="switchSection('nodes')"
          >
            <span class="side-label">节点管理</span>
            <span class="side-pill mono">{{ nodes.length }}</span>
          </button>

          <button
            type="button"
            class="side-item"
            :class="{ active: activeSection === 'updates' }"
            @click="switchSection('updates')"
          >
            <span class="side-label">版本升级与分发</span>
            <span v-if="failedUpdateNodes.length > 0" class="side-pill side-pill-red mono">
              {{ failedUpdateNodes.length }} 失败
            </span>
            <span v-else-if="outdatedOnlineNodes.length > 0" class="side-pill side-pill-amber mono">
              {{ outdatedOnlineNodes.length }} 待升
            </span>
          </button>

          <button
            type="button"
            class="side-item"
            :class="{ active: activeSection === 'policy' }"
            @click="switchSection('policy')"
          >
            <span class="side-label">目标访问策略</span>
            <span class="side-pill" :class="targetPolicy.enabled ? 'side-pill-green' : ''">
              {{ targetPolicy.enabled ? '已启用' : '关闭' }}
            </span>
          </button>
        </nav>

        <div class="sidebar-footer">
          <button
            type="button"
            class="btn btn-outline btn-sm sidebar-btn"
            :disabled="loading"
            @click="loadData(adminToken)"
          >
            {{ loading ? '同步中...' : '立即刷新' }}
          </button>
          <button type="button" class="btn btn-outline btn-sm sidebar-btn" @click="handleLogout">
            退出登录
          </button>
        </div>
      </aside>

      <!-- Right Main Workspace -->
      <section class="admin-main">
        <div v-if="actionMsg" class="info-banner">{{ actionMsg }}</div>
        <div v-if="actionErr" class="error-banner">{{ actionErr }}</div>

        <!-- SECTION 1: OVERVIEW -->
        <template v-if="activeSection === 'overview'">
          <div class="section-head">
            <div>
              <h2 class="section-title">运行概览</h2>
              <p class="section-sub">控制面健康状态、全网并发负载与运维待办摘要（每 8 秒自动同步）</p>
            </div>
            <div class="header-actions">
              <button type="button" class="btn btn-success btn-sm" @click="openCreateModal">
                + 新增节点
              </button>
              <button type="button" class="btn btn-primary btn-sm" @click="switchSection('updates')">
                管理 Agent 升级
              </button>
            </div>
          </div>

          <div v-if="overview" class="nodes-kpi">
            <div class="card kpi-card">
              <div class="kpi-title">在线 / 离线节点 (共 {{ overview.total_nodes }})</div>
              <div class="kpi-num mono">
                <span style="color: #16a34a">{{ overview.online_nodes }}</span>
                <span class="text-muted"> / {{ overview.offline_nodes }}</span>
              </div>
            </div>
            <div class="card kpi-card">
              <div class="kpi-title">全网并发运行 / 总容量</div>
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

          <div class="overview-grid">
            <div v-if="overview" class="card">
              <div class="card-header">
                <span>控制面基础设施</span>
                <span class="badge badge-blue mono">{{ overview.version }}</span>
              </div>
              <div class="card-body kv-list">
                <div class="kv-row">
                  <span class="kv-k">数据库引擎</span>
                  <span class="kv-v mono">{{ overview.db_driver.toUpperCase() }}</span>
                </div>
                <div class="kv-row">
                  <span class="kv-k">NATS 接入地址</span>
                  <span class="kv-v mono">{{ overview.public_nats_url || overview.nats_url }}</span>
                </div>
                <div class="kv-row">
                  <span class="kv-k">单节点队列上限</span>
                  <span class="kv-v mono">{{ overview.max_queue_per_node }} 任务</span>
                </div>
                <div v-if="overview.nats_account_pub" class="kv-row">
                  <span class="kv-k">NATS Account 公钥</span>
                  <span class="kv-v mono">{{ overview.nats_account_pub.slice(0, 18) }}…</span>
                </div>
                <div class="kv-row">
                  <span class="kv-k">DNS 固定上游</span>
                  <span class="kv-v mono">172.16.255.53 (DN11 IPv4)</span>
                </div>
              </div>
            </div>

            <div class="card">
              <div class="card-header">
                <span>运维状态与快捷入口</span>
              </div>
              <div class="card-body status-links">
                <div class="quick-item" @click="switchSection('nodes')">
                  <div>
                    <div class="quick-title">节点健康状态</div>
                    <div class="quick-desc">
                      {{ offlineNodes.length > 0 ? `${offlineNodes.length} 个节点离线` : '全部节点在线运行正常' }}
                      <template v-if="disabledNodes.length > 0"> · {{ disabledNodes.length }} 个已禁用</template>
                    </div>
                  </div>
                  <span class="badge" :class="offlineNodes.length > 0 ? 'badge-amber' : 'badge-green'">
                    查看节点 →
                  </span>
                </div>

                <div class="quick-item" @click="switchSection('updates')">
                  <div>
                    <div class="quick-title">Agent 版本与二进制托管</div>
                    <div class="quick-desc">
                      已托管 {{ overview?.hosted_binaries?.length ?? 0 }} 个平台文件
                      <template v-if="outdatedOnlineNodes.length > 0">
                        · {{ outdatedOnlineNodes.length }} 个在线节点可升级
                      </template>
                      <template v-if="failedUpdateNodes.length > 0">
                        · {{ failedUpdateNodes.length }} 个更新失败
                      </template>
                    </div>
                  </div>
                  <span
                    class="badge"
                    :class="
                      failedUpdateNodes.length > 0
                        ? 'badge-red'
                        : outdatedOnlineNodes.length > 0
                          ? 'badge-amber'
                          : 'badge-green'
                    "
                  >
                    前往升级 →
                  </span>
                </div>

                <div class="quick-item" @click="switchSection('policy')">
                  <div>
                    <div class="quick-title">全局目标访问限制</div>
                    <div class="quick-desc">
                      {{
                        targetPolicy.enabled
                          ? `已启用：允许 ${targetPolicy.allow_rules.length} 条 · 禁止 ${targetPolicy.deny_rules.length} 条`
                          : '当前未启用，放行全部 IPv4 与域名目标'
                      }}
                    </div>
                  </div>
                  <span class="badge" :class="targetPolicy.enabled ? 'badge-blue' : 'badge-slate'">
                    配置策略 →
                  </span>
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- SECTION 2: NODES MANAGEMENT -->
        <template v-else-if="activeSection === 'nodes'">
          <div class="section-head">
            <div>
              <h2 class="section-title">节点管理</h2>
              <p class="section-sub">创建与配置探测节点、签发 NATS JWT 凭据并查看实时遥测</p>
            </div>
            <div class="header-actions">
              <button type="button" class="btn btn-success btn-sm" @click="openCreateModal">
                + 新增节点
              </button>
            </div>
          </div>

          <div class="card">
            <div class="card-header filter-toolbar">
              <div class="tab-group">
                <button
                  type="button"
                  class="tab-btn"
                  :class="{ active: nodeStatusFilter === 'all' }"
                  @click="nodeStatusFilter = 'all'"
                >
                  全部 ({{ nodes.length }})
                </button>
                <button
                  type="button"
                  class="tab-btn"
                  :class="{ active: nodeStatusFilter === 'online' }"
                  @click="nodeStatusFilter = 'online'"
                >
                  在线 ({{ onlineNodes.length }})
                </button>
                <button
                  type="button"
                  class="tab-btn"
                  :class="{ active: nodeStatusFilter === 'offline' }"
                  @click="nodeStatusFilter = 'offline'"
                >
                  离线 ({{ offlineNodes.length }})
                </button>
                <button
                  v-if="disabledNodes.length > 0"
                  type="button"
                  class="tab-btn"
                  :class="{ active: nodeStatusFilter === 'disabled' }"
                  @click="nodeStatusFilter = 'disabled'"
                >
                  已禁用 ({{ disabledNodes.length }})
                </button>
              </div>

              <input
                v-model="nodeKeyword"
                type="text"
                class="search-input"
                placeholder="搜索节点名称 / UUID / 区域 / IPv4 / ASN"
              />
            </div>

            <div class="data-table-wrap">
              <table class="data-table">
                <thead>
                  <tr>
                    <th>状态</th>
                    <th>节点名称 / UUID</th>
                    <th>区域 / 网络组 / ASN</th>
                    <th>接入 IPv4</th>
                    <th>并发 / 队列</th>
                    <th>资源 / 心跳</th>
                    <th>管理操作</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="n in filteredNodes" :key="n.id" :class="{ 'row-disabled': !n.enabled }">
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
                      <div class="mono text-sm" style="margin-top: 2px">
                        {{ n.asn ? `AS${n.asn}` : '-' }}
                      </div>
                    </td>
                    <td class="mono text-sm">
                      {{ n.ipv4 || '-' }}
                    </td>
                    <td>
                      <div class="mono text-sm">
                        并发 {{ n.active_tasks }}/{{ n.max_concurrency }} · 队列 {{ n.queue_depth }}/{{ overview?.max_queue_per_node ?? 20 }}
                      </div>
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
                    <td class="mono text-sm">
                      <div>CPU {{ n.cpu_percent.toFixed(1) }}% · 内存 {{ n.mem_percent.toFixed(1) }}%</div>
                      <div class="text-muted">
                        {{ n.version || '-' }} · {{ formatTime(n.last_seen_unix_ms) }}
                      </div>
                    </td>
                    <td>
                      <div class="row-actions">
                        <button type="button" class="btn btn-primary btn-sm" @click="openDeployModal(n)">
                          部署向导
                        </button>
                        <button type="button" class="btn btn-outline btn-sm" @click="openEditModal(n)">
                          编辑
                        </button>
                        <button
                          v-if="n.status === 'online'"
                          type="button"
                          class="btn btn-outline btn-sm"
                          @click="handleDisconnect(n)"
                        >
                          重连
                        </button>
                        <button type="button" class="btn btn-outline btn-sm" @click="handleRotateToken(n)">
                          重签Creds
                        </button>
                        <button type="button" class="btn btn-danger btn-sm" @click="handleDelete(n)">
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                  <tr v-if="filteredNodes.length === 0">
                    <td colspan="7" class="empty-nodes">
                      {{ nodes.length === 0 ? '暂无节点记录，请点击右上角「+ 新增节点」开始部署。' : '没有符合筛选条件的节点。' }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>

        <!-- SECTION 3: AGENT UPDATES & BINARY HOSTING -->
        <template v-else-if="activeSection === 'updates'">
          <div class="section-head">
            <div>
              <h2 class="section-title">版本升级与分发</h2>
              <p class="section-sub">查看服务端托管的 Agent 二进制文件，按节点或批量下发在线平滑升级</p>
            </div>
            <div class="header-actions">
              <button
                v-if="selectedUpdateNodeIDs.length > 0"
                type="button"
                class="btn btn-success btn-sm"
                @click="openUpdateModal(null, selectedUpdateNodeIDs)"
              >
                升级已选 ({{ selectedUpdateNodeIDs.length }})
              </button>
              <button
                type="button"
                class="btn btn-primary btn-sm"
                :disabled="onlineNodes.length === 0"
                @click="openUpdateModal(null)"
              >
                批量升级全部在线节点 ({{ onlineNodes.length }})
              </button>
            </div>
          </div>

          <div v-if="overview" class="card">
            <div class="card-header">
              <span>服务端托管分发文件</span>
              <span class="mono text-muted">GET /downloads/&lt;binary&gt; · GET /scripts/install.sh</span>
            </div>
            <div class="card-body hosted-panel">
              <div class="hosted-bins">
                <span
                  v-for="bin in overview.hosted_binaries"
                  :key="bin"
                  class="badge badge-green mono"
                >
                  ✓ {{ bin }}
                </span>
                <span v-if="!overview.hosted_binaries?.length" class="badge badge-amber">
                  未检测到托管的 Agent 二进制文件（请构建放入 agent_bin_dir 或在升级时填写自定义下载地址）
                </span>
              </div>
              <div v-if="overview.agent_download_base_url" class="text-muted mono" style="margin-top: 8px">
                下载基址: {{ overview.agent_download_base_url }}
              </div>
            </div>
          </div>

          <div class="card">
            <div class="card-header filter-toolbar">
              <span>在线与离线节点版本状态</span>
              <div class="header-actions">
                <button
                  type="button"
                  class="link-btn"
                  :disabled="onlineNodes.length === 0"
                  @click="selectAllOnlineForUpdate"
                >
                  全选在线
                </button>
                <button
                  v-if="outdatedOnlineNodes.length > 0"
                  type="button"
                  class="link-btn"
                  @click="selectOutdatedForUpdate"
                >
                  仅选待升级 ({{ outdatedOnlineNodes.length }})
                </button>
                <button
                  v-if="selectedUpdateNodeIDs.length > 0"
                  type="button"
                  class="link-btn"
                  @click="selectedUpdateNodeIDs = []"
                >
                  清空选择
                </button>
              </div>
            </div>

            <div class="data-table-wrap">
              <table class="data-table">
                <thead>
                  <tr>
                    <th style="width: 42px; text-align: center">选择</th>
                    <th>节点</th>
                    <th>运行状态</th>
                    <th>平台</th>
                    <th>当前版本</th>
                    <th>升级状态与详情</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="n in nodes" :key="n.id">
                    <td style="text-align: center">
                      <input
                        type="checkbox"
                        :disabled="n.status !== 'online'"
                        :checked="selectedUpdateNodeIDs.includes(n.id)"
                        @change="toggleUpdateSelect(n.id)"
                      />
                    </td>
                    <td>
                      <div style="font-weight: 600">{{ n.name }}</div>
                      <div class="mono text-muted">{{ n.region }} · {{ n.group }}</div>
                    </td>
                    <td>
                      <span class="badge" :class="n.status === 'online' ? 'badge-green' : 'badge-slate'">
                        <span :class="n.status === 'online' ? 'dot-online' : 'dot-offline'"></span>
                        {{ n.status === 'online' ? '在线' : '离线' }}
                      </span>
                    </td>
                    <td class="mono text-sm">
                      {{ n.os || '?' }}/{{ n.arch || '?' }}
                    </td>
                    <td class="mono text-sm">
                      <span>{{ n.version || '-' }}</span>
                      <span v-if="isNodeOutdated(n)" class="badge badge-amber" style="margin-left: 6px">
                        可升级
                      </span>
                    </td>
                    <td>
                      <template v-if="n.update_status">
                        <div class="update-detail-row">
                          <span class="badge" :class="updateStateBadgeClass(n.update_status.state)">
                            {{ updateStateLabel(n.update_status.state) }}
                          </span>
                          <span v-if="n.update_status.target_version" class="mono text-muted">
                            → {{ n.update_status.target_version }}
                          </span>
                          <span v-if="n.update_status.updated_at_ms" class="mono text-muted">
                            ({{ formatTime(n.update_status.updated_at_ms) }})
                          </span>
                        </div>
                        <div v-if="n.update_status.message" class="update-msg">
                          {{ n.update_status.message }}
                        </div>
                      </template>
                      <span v-else class="text-muted">尚无升级记录</span>
                    </td>
                    <td>
                      <button
                        type="button"
                        class="btn btn-success btn-sm"
                        :disabled="n.status !== 'online'"
                        @click="openUpdateModal(n)"
                      >
                        下发更新
                      </button>
                    </td>
                  </tr>
                  <tr v-if="nodes.length === 0">
                    <td colspan="7" class="empty-nodes">暂无节点数据</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>

        <!-- SECTION 4: TARGET ACCESS POLICY -->
        <template v-else-if="activeSection === 'policy'">
          <div class="section-head">
            <div>
              <h2 class="section-title">目标访问策略</h2>
              <p class="section-sub">控制全局允许或禁止探测的 IPv4 CIDR 网段与域名通配符，保存后实时同步全集群内存缓存</p>
            </div>
            <div class="header-actions">
              <button
                v-if="policyDirty"
                type="button"
                class="btn btn-outline btn-sm"
                :disabled="policySaving"
                @click="resetPolicyDraft"
              >
                撤销修改
              </button>
              <button
                type="button"
                class="btn btn-primary btn-sm"
                :disabled="policySaving"
                @click="saveTargetPolicy"
              >
                {{ policySaving ? '保存中...' : '保存并生效' }}
              </button>
            </div>
          </div>

          <div class="card">
            <div class="card-header">
              <div class="admin-title-group">
                <span>策略总开关</span>
                <span class="badge" :class="policyEnabled ? 'badge-green' : 'badge-slate'">
                  {{ policyEnabled ? '已启用' : '未启用' }}
                </span>
                <span v-if="policyDirty" class="badge badge-amber">有未保存更改</span>
              </div>
              <span v-if="targetPolicy.updated_at_ms" class="mono text-muted">
                上次保存: {{ formatTime(targetPolicy.updated_at_ms) }}
              </span>
            </div>

            <div class="card-body">
              <label class="field-check" style="margin-bottom: 14px">
                <input
                  v-model="policyEnabled"
                  type="checkbox"
                  @change="policyDirty = true"
                />
                <span>启用全局测试目标限制（禁止列表优先级高于允许列表；允许列表留空则放行除禁止列表外的所有 IPv4 / 域名）</span>
              </label>

              <div class="form-grid">
                <label class="field-item">
                  <div class="rule-label-row">
                    <span>允许列表（Allow Rules）</span>
                    <span class="badge badge-green mono">{{ allowRuleCount }} 条</span>
                  </div>
                  <textarea
                    v-model="policyAllowText"
                    rows="8"
                    class="policy-textarea mono"
                    placeholder="每行一条 IPv4 / CIDR / 域名通配符（# 开头为注释），例如：&#10;172.16.0.0/16&#10;*.dn11.org&#10;example.dn11"
                    @input="policyDirty = true"
                  ></textarea>
                </label>

                <label class="field-item">
                  <div class="rule-label-row">
                    <span>禁止列表（Deny Rules）</span>
                    <span class="badge badge-red mono">{{ denyRuleCount }} 条</span>
                  </div>
                  <textarea
                    v-model="policyDenyText"
                    rows="8"
                    class="policy-textarea mono"
                    placeholder="每行一条，优先拦截匹配目标，例如：&#10;127.0.0.0/8&#10;169.254.0.0/16&#10;*.internal.local"
                    @input="policyDirty = true"
                  ></textarea>
                </label>
              </div>
            </div>
          </div>
        </template>
      </section>

      <!-- Create Node Modal -->
      <div v-if="showCreateModal" class="modal-backdrop" @click.self="showCreateModal = false">
        <div class="card modal-card">
          <div class="card-header">
            <span>新增监测节点（写入数据库并签发专属凭据）</span>
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
                创建并打开部署向导
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
                <span>启用该节点（取消勾选将不再调度探测任务）</span>
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

      <!-- Tabbed Deploy Wizard Modal -->
      <div v-if="deployNode" class="modal-backdrop" @click.self="deployNode = null">
        <div class="card modal-card wide">
          <div class="card-header">
            <span>节点部署向导：「{{ deployNode.name }}」</span>
            <div class="header-actions">
              <span v-if="copiedTip" class="badge badge-green">{{ copiedTip }}</span>
              <button type="button" class="btn btn-outline btn-sm" @click="deployNode = null">关闭</button>
            </div>
          </div>
          <div class="card-body">
            <div class="form-grid" style="margin-bottom: 12px">
              <label class="field-item full-span">
                <span>NATS WebSocket 地址（ws:// 或 wss://）</span>
                <input v-model="deployNATSAddr" type="text" class="mono" />
              </label>
            </div>

            <div class="tab-group" style="margin-bottom: 12px">
              <button type="button" class="tab-btn" :class="{ active: deployTab === 'systemd' }" @click="deployTab = 'systemd'">
                一键安装 (systemd)
              </button>
              <button type="button" class="tab-btn" :class="{ active: deployTab === 'docker' }" @click="deployTab = 'docker'">
                Docker Compose
              </button>
              <button type="button" class="tab-btn" :class="{ active: deployTab === 'creds' }" @click="deployTab = 'creds'">
                NATS .creds 凭据
              </button>
              <button type="button" class="tab-btn" :class="{ active: deployTab === 'yaml' }" @click="deployTab = 'yaml'">
                agent.yaml 配置
              </button>
              <button type="button" class="tab-btn" :class="{ active: deployTab === 'uninstall' }" @click="deployTab = 'uninstall'">
                卸载命令
              </button>
            </div>

            <div v-if="deployTab === 'systemd'" class="code-section">
              <div class="code-head">
                <span>托管安装脚本（自动下载二进制、拉取 .creds 并配置 systemd 守护）</span>
                <button type="button" class="btn btn-primary btn-sm" @click="copyText('安装命令', installCmd)">
                  复制安装命令
                </button>
              </div>
              <pre class="mono guide-pre">{{ installCmd }}</pre>
            </div>

            <div v-else-if="deployTab === 'docker'" class="code-section">
              <div class="code-head">
                <span>docker-compose.yml（请将 .creds 凭据保存为同目录 agent.creds）</span>
                <button type="button" class="btn btn-primary btn-sm" @click="copyText('Docker Compose', dockerComposeAgentYaml)">
                  复制 Compose
                </button>
              </div>
              <pre class="mono guide-pre">{{ dockerComposeAgentYaml }}</pre>
            </div>

            <div v-else-if="deployTab === 'creds'" class="code-section">
              <div class="code-head">
                <span>节点专属 NATS JWT/NKey 凭据 (/etc/11dog/agent.creds)</span>
                <button type="button" class="btn btn-primary btn-sm" @click="copyText('NATS .creds', deployNode.creds || '')">
                  复制 .creds
                </button>
              </div>
              <pre class="mono guide-pre">{{ deployNode.creds || '未签发，请点击重签Creds' }}</pre>
            </div>

            <div v-else-if="deployTab === 'yaml'" class="code-section">
              <div class="code-head">
                <span>手动配置文件 (/etc/11dog/agent.yaml)</span>
                <button type="button" class="btn btn-primary btn-sm" @click="copyText('YAML 配置', minimalAgentYaml)">
                  复制配置
                </button>
              </div>
              <pre class="mono guide-pre">{{ minimalAgentYaml }}</pre>
            </div>

            <div v-else class="code-section">
              <div class="code-head">
                <span>一键卸载 11dog-agent 服务与配置文件</span>
                <button type="button" class="btn btn-danger btn-sm" @click="copyText('卸载命令', uninstallCmd)">
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
                  ? `下发 Agent 更新：${updateTargetNode.name}`
                  : updateTargetNodeIDs.length > 0
                    ? `批量下发 Agent 更新（已选 ${updateTargetNodeIDs.length} 个在线节点）`
                    : `批量下发 Agent 更新（全部 ${onlineNodes.length} 个在线节点）`
              }}
            </span>
            <button type="button" class="btn btn-outline btn-sm" @click="showUpdateModal = false">关闭</button>
          </div>
          <form class="card-body modal-form" @submit.prevent="submitAgentUpdate">
            <p class="deploy-hint">
              留空下载地址时，服务端将自动按节点 OS/Arch 匹配托管目录（<code class="mono">/downloads/11dog-agent-&lt;os&gt;-&lt;arch&gt;</code>）并自动附加 SHA-256 校验值。
            </p>
            <div class="form-grid">
              <label class="field-item full-span">
                <span>目标版本号（留空默认服务端版本 {{ overview?.version || '-' }}）</span>
                <input v-model="updateForm.version" type="text" class="mono" placeholder="例如：1.1.0" />
              </label>
              <label class="field-item full-span">
                <span>自定义下载地址（可选）</span>
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
                <span>强制更新（即使当前版本与目标版本一致也重新下载替换并重启）</span>
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
    </div>
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

.admin-shell {
  display: grid;
  grid-template-columns: 230px minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}

.admin-sidebar {
  position: sticky;
  top: 70px;
  padding: 14px;
  margin-bottom: 0;
}

.sidebar-brand {
  padding-bottom: 12px;
  margin-bottom: 10px;
  border-bottom: 1px solid var(--border-color);
}

.sidebar-title {
  font-size: 15px;
  font-weight: 700;
  color: var(--text-primary);
}

.sidebar-ver {
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 2px;
}

.sidebar-nav {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.side-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  border: 1px solid transparent;
  background: transparent;
  border-radius: 6px;
  padding: 9px 10px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-secondary);
  cursor: pointer;
  text-align: left;
  transition: all 0.15s ease;
}

.side-item:hover {
  background: #f8fafc;
  color: var(--text-primary);
}

.side-item.active {
  background: var(--brand-blue-light);
  border-color: #bfdbfe;
  color: var(--brand-blue);
}

.side-pill {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 999px;
  background: #f1f5f9;
  color: var(--text-secondary);
}

.side-pill-green {
  background: #dcfce7;
  color: #15803d;
}

.side-pill-amber {
  background: #fef3c7;
  color: #b45309;
}

.side-pill-red {
  background: #fee2e2;
  color: #b91c1c;
}

.sidebar-footer {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--border-color);
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.sidebar-btn {
  width: 100%;
}

.admin-main {
  min-width: 0;
}

.section-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 14px;
}

.section-title {
  margin: 0;
  font-size: 18px;
  font-weight: 700;
}

.section-sub {
  margin: 2px 0 0;
  font-size: 12px;
  color: var(--text-secondary);
}

.admin-title-group {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
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
  font-size: 21px;
  font-weight: 800;
  margin-top: 4px;
}

.text-stats {
  font-size: 15px;
}

.overview-grid {
  display: grid;
  grid-template-columns: 1fr 1.2fr;
  gap: 14px;
}

.kv-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.kv-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding-bottom: 8px;
  border-bottom: 1px dashed var(--border-color);
  font-size: 13px;
}

.kv-row:last-child {
  border-bottom: none;
  padding-bottom: 0;
}

.kv-k {
  color: var(--text-secondary);
}

.kv-v {
  font-weight: 600;
  color: var(--text-primary);
  text-align: right;
  word-break: break-all;
}

.status-links {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.quick-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: #f8fafc;
  cursor: pointer;
  transition: all 0.15s ease;
}

.quick-item:hover {
  border-color: var(--brand-blue);
  background: #eff6ff;
}

.quick-title {
  font-weight: 600;
  font-size: 13px;
  color: var(--text-primary);
}

.quick-desc {
  font-size: 12px;
  color: var(--text-secondary);
  margin-top: 2px;
}

.filter-toolbar {
  flex-wrap: wrap;
}

.tab-group {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.tab-btn {
  border: 1px solid var(--border-color);
  background: #f8fafc;
  color: var(--text-secondary);
  border-radius: 5px;
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.tab-btn.active {
  background: var(--brand-blue-light);
  border-color: var(--brand-blue);
  color: var(--brand-blue);
}

.search-input {
  border: 1px solid var(--border-strong);
  border-radius: 5px;
  padding: 5px 10px;
  font-size: 12px;
  width: 240px;
  outline: none;
}

.search-input:focus {
  border-color: var(--brand-blue);
}

.hosted-panel {
  padding: 14px 20px;
}

.hosted-bins {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.header-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.link-btn {
  border: none;
  background: none;
  padding: 2px 4px;
  font-size: 12px;
  color: var(--brand-blue);
  cursor: pointer;
}

.link-btn:hover:not(:disabled) {
  text-decoration: underline;
}

.link-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
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

.update-detail-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.update-msg {
  font-size: 11px;
  color: var(--text-secondary);
  margin-top: 3px;
  max-width: 300px;
}

.rule-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.text-muted {
  color: var(--text-muted);
  font-size: 11px;
}

.text-sm {
  font-size: 12px;
}

.mini-bar {
  width: 110px;
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
  margin-bottom: 0;
}

.code-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
}

.guide-pre {
  background: #0f172a;
  color: #e2e8f0;
  padding: 12px;
  border-radius: 6px;
  margin: 0;
  overflow-x: auto;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
}

@media (max-width: 960px) {
  .admin-shell {
    grid-template-columns: 1fr;
  }
  .admin-sidebar {
    position: static;
  }
  .sidebar-nav {
    flex-direction: row;
    flex-wrap: wrap;
  }
  .side-item {
    width: auto;
  }
  .sidebar-footer {
    flex-direction: row;
  }
  .nodes-kpi {
    grid-template-columns: repeat(2, 1fr);
  }
  .overview-grid,
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
