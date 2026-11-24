<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ContinuousNodeStats, ContinuousSession, ContinuousTickPoint } from '@/composables/useProbeTask'
import { formatMs, latencyColor, statusLabel } from '@/utils/format'

const props = defineProps<{
  session: ContinuousSession
  checkedChartIDs: string[]
}>()

const emit = defineEmits<{
  (e: 'update:checkedChartIDs', ids: string[]): void
}>()

const filterTab = ref<'all' | 'ok' | 'loss' | 'down'>('all')
const keyword = ref('')
const sortKey = ref<'node' | 'last' | 'avg' | 'loss' | 'jitter'>('avg')
const sortAsc = ref(true)

const rows = computed(() => Object.values(props.session.nodes))

const counts = computed(() => {
  let ok = 0
  let loss = 0
  let down = 0
  for (const r of rows.value) {
    if (r.last_status === 'completed') ok++
    else if (r.sent > 0) down++
    if (r.lost > 0) loss++
  }
  return { all: rows.value.length, ok, loss, down }
})

const filteredRows = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const list = rows.value.filter((r) => {
    if (filterTab.value === 'ok' && r.last_status !== 'completed') return false
    if (filterTab.value === 'loss' && r.lost === 0) return false
    if (filterTab.value === 'down' && (r.last_status === 'completed' || r.sent === 0)) return false
    if (kw) {
      const hay = `${r.node_name} ${r.region} ${r.group} ${r.resolved_ip ?? ''} ${r.resolved_dn11?.owner ?? ''} AS${r.node_asn}`.toLowerCase()
      if (!hay.includes(kw)) return false
    }
    return true
  })

  return [...list].sort((a, b) => {
    let cmp = 0
    if (sortKey.value === 'node') {
      cmp = `${a.region}-${a.node_name}`.localeCompare(`${b.region}-${b.node_name}`)
    } else if (sortKey.value === 'last') {
      cmp = (a.last_ms ?? Number.MAX_SAFE_INTEGER) - (b.last_ms ?? Number.MAX_SAFE_INTEGER)
    } else if (sortKey.value === 'loss') {
      cmp = a.loss_percent - b.loss_percent
    } else if (sortKey.value === 'jitter') {
      cmp = (a.jitter_ms ?? Number.MAX_SAFE_INTEGER) - (b.jitter_ms ?? Number.MAX_SAFE_INTEGER)
    } else {
      cmp = (a.avg_ms ?? Number.MAX_SAFE_INTEGER) - (b.avg_ms ?? Number.MAX_SAFE_INTEGER)
    }
    return sortAsc.value ? cmp : -cmp
  })
})

function toggleSort(key: 'node' | 'last' | 'avg' | 'loss' | 'jitter') {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value
  } else {
    sortKey.value = key
    sortAsc.value = true
  }
}

function toggleChartNode(nodeID: string) {
  const set = new Set(props.checkedChartIDs)
  if (set.has(nodeID)) set.delete(nodeID)
  else set.add(nodeID)
  emit('update:checkedChartIDs', Array.from(set))
}

function selectTop6() {
  emit(
    'update:checkedChartIDs',
    rows.value.slice(0, 6).map((n) => n.node_id)
  )
}

function selectAllChart() {
  emit(
    'update:checkedChartIDs',
    rows.value.map((n) => n.node_id)
  )
}

function clearChartSelection() {
  emit('update:checkedChartIDs', [])
}

function recentSparkBars(n: ContinuousNodeStats): (ContinuousTickPoint | null)[] {
  const tail = n.samples.slice(-30)
  const padded: (ContinuousTickPoint | null)[] = []
  for (let i = 0; i < 30 - tail.length; i++) {
    padded.push(null)
  }
  return padded.concat(tail)
}

function barHeightPct(pt: ContinuousTickPoint | null, nodeMax: number | null): number {
  if (!pt) return 0
  if (pt.ms === null) return 100
  const cap = Math.max(30, nodeMax ?? 100)
  return Math.max(18, Math.min(100, Math.round((pt.ms / cap) * 100)))
}

function barColor(pt: ContinuousTickPoint | null): string {
  if (!pt) return '#e2e8f0'
  if (pt.ms === null) {
    return pt.status === 'overloaded' || pt.status === 'expired' ? '#7c3aed' : '#ef4444'
  }
  return latencyColor(pt.ms)
}

function barTitle(pt: ContinuousTickPoint | null): string {
  if (!pt) return '暂无数据'
  const t = new Date(pt.ts).toLocaleTimeString()
  if (pt.ms === null) return `#${pt.round} (${t}) ${statusLabel(pt.status)}`
  return `#${pt.round} (${t}) ${formatMs(pt.ms)}`
}

function httpCodeClass(code: number): string {
  if (code >= 200 && code < 300) return 'badge-green'
  if (code >= 300 && code < 400) return 'badge-blue'
  if (code >= 400 && code < 500) return 'badge-amber'
  return 'badge-red'
}
</script>

<template>
  <div class="card">
    <div class="card-header table-toolbar">
      <div class="tab-group">
        <button type="button" class="tab-btn" :class="{ active: filterTab === 'all' }" @click="filterTab = 'all'">
          全部 ({{ counts.all }})
        </button>
        <button type="button" class="tab-btn" :class="{ active: filterTab === 'ok' }" @click="filterTab = 'ok'">
          正常 ({{ counts.ok }})
        </button>
        <button type="button" class="tab-btn" :class="{ active: filterTab === 'loss' }" @click="filterTab = 'loss'">
          有丢包 ({{ counts.loss }})
        </button>
        <button type="button" class="tab-btn" :class="{ active: filterTab === 'down' }" @click="filterTab = 'down'">
          当前异常 ({{ counts.down }})
        </button>
      </div>

      <div class="right-tools">
        <div class="chart-quick-btns">
          <button type="button" class="link-btn" @click="selectTop6">对比前6个</button>
          <button type="button" class="link-btn" @click="selectAllChart">全选曲线</button>
          <button type="button" class="link-btn" @click="clearChartSelection">仅看均值</button>
        </div>
        <input
          v-model="keyword"
          type="text"
          class="search-input"
          placeholder="搜索节点 / 区域 / IP / ASN"
        />
      </div>
    </div>

    <div class="data-table-wrap">
      <table class="data-table">
        <thead>
          <tr>
            <th style="width: 44px; text-align: center" title="勾选以在上方折线图中显示该节点曲线">曲线</th>
            <th class="sortable" @click="toggleSort('node')">监测点</th>
            <th>响应 IP / DN11 归属</th>
            <th class="sortable" @click="toggleSort('last')">最新延迟</th>
            <th class="sortable" @click="toggleSort('loss')">发包 / 丢包 / 丢包率</th>
            <th class="sortable" @click="toggleSort('avg')">最快 / 平均 / 最慢</th>
            <th class="sortable" @click="toggleSort('jitter')">抖动</th>
            <th style="min-width: 210px">最近 30 次实时波动</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in filteredRows" :key="row.node_id">
            <td style="text-align: center">
              <input
                type="checkbox"
                :checked="checkedChartIDs.includes(row.node_id)"
                @change="toggleChartNode(row.node_id)"
              />
            </td>
            <td>
              <div class="node-cell">
                <span class="region-tag">{{ row.region }}</span>
                <div>
                  <div class="node-title">{{ row.node_name }}</div>
                  <div class="node-sub mono">
                    {{ row.group }}<template v-if="row.node_asn"> · AS{{ row.node_asn }}</template>
                  </div>
                </div>
              </div>
            </td>
            <td>
              <div v-if="row.resolved_ip" class="mono ip-val">{{ row.resolved_ip }}</div>
              <div v-if="row.resolved_dn11" class="dn11-tag">
                AS{{ row.resolved_dn11.asn }} · {{ row.resolved_dn11.owner }}
              </div>
              <span v-else-if="!row.resolved_ip" class="text-muted">-</span>
            </td>
            <td>
              <div class="last-cell">
                <span
                  v-if="row.last_ms !== null"
                  class="lat-pill"
                  :style="{ background: latencyColor(row.last_ms) }"
                >
                  {{ formatMs(row.last_ms) }}
                </span>
                <span v-else-if="row.sent === 0" class="badge badge-blue">等待中</span>
                <span
                  v-else
                  class="badge"
                  :class="row.last_status === 'overloaded' || row.last_status === 'expired' ? 'badge-purple' : 'badge-red'"
                >
                  {{ statusLabel(row.last_status) }}
                </span>
                <span
                  v-if="session.probeType === 'http' && row.last_http_code"
                  class="badge mono"
                  :class="httpCodeClass(row.last_http_code)"
                >
                  {{ row.last_http_code }}
                </span>
              </div>
            </td>
            <td class="mono">
              <span>{{ row.sent }}</span> /
              <span :class="{ 'loss-warn': row.lost > 0 }">{{ row.lost }}</span> /
              <span :class="{ 'loss-warn': row.loss_percent > 0 }">{{ row.loss_percent.toFixed(1) }}%</span>
            </td>
            <td class="mono text-sm">
              <template v-if="row.received > 0">
                {{ formatMs(row.min_ms) }} / <strong>{{ formatMs(row.avg_ms) }}</strong> / {{ formatMs(row.max_ms) }}
              </template>
              <span v-else class="text-muted">-</span>
            </td>
            <td class="mono text-sm">
              {{ row.jitter_ms !== null ? formatMs(row.jitter_ms) : '-' }}
            </td>
            <td>
              <div class="sparkbar-row">
                <div
                  v-for="(pt, idx) in recentSparkBars(row)"
                  :key="idx"
                  class="sparkbar-slot"
                  :title="barTitle(pt)"
                >
                  <div
                    v-if="pt"
                    class="sparkbar-fill"
                    :class="{ dropped: pt.ms === null }"
                    :style="{
                      height: `${barHeightPct(pt, row.max_ms)}%`,
                      background: barColor(pt)
                    }"
                  ></div>
                </div>
              </div>
            </td>
          </tr>
          <tr v-if="filteredRows.length === 0">
            <td colspan="8" class="empty-row">暂无匹配的持续测试数据</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.table-toolbar {
  flex-wrap: wrap;
  gap: 8px;
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

.right-tools {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.chart-quick-btns {
  display: flex;
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

.link-btn:hover {
  text-decoration: underline;
}

.search-input {
  border: 1px solid var(--border-strong);
  border-radius: 5px;
  padding: 5px 10px;
  font-size: 12px;
  width: 210px;
  outline: none;
}

.node-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}

.region-tag {
  background: #eef2ff;
  color: #3730a3;
  font-size: 11px;
  font-weight: 700;
  padding: 2px 6px;
  border-radius: 4px;
  white-space: nowrap;
}

.node-title {
  font-weight: 600;
}

.node-sub {
  font-size: 11px;
  color: var(--text-muted);
}

.ip-val {
  font-weight: 600;
}

.dn11-tag {
  font-size: 11px;
  color: #0369a1;
  background: #f0f9ff;
  border: 1px solid #bae6fd;
  display: inline-block;
  padding: 1px 5px;
  border-radius: 3px;
  margin-top: 2px;
}

.last-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}

.loss-warn {
  color: #dc2626;
  font-weight: 700;
}

.text-sm {
  font-size: 12px;
}

.text-muted {
  color: var(--text-muted);
}

.sparkbar-row {
  display: flex;
  align-items: flex-end;
  gap: 2px;
  height: 24px;
  padding: 2px 4px;
  background: #f8fafc;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  width: 204px;
}

.sparkbar-slot {
  width: 4.5px;
  height: 100%;
  display: flex;
  align-items: flex-end;
}

.sparkbar-fill {
  width: 100%;
  border-radius: 1px;
  transition: height 0.15s ease;
}

.sparkbar-fill.dropped {
  opacity: 0.75;
}

.empty-row {
  text-align: center;
  padding: 28px;
  color: var(--text-muted);
}
</style>
