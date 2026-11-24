<script setup lang="ts">
import { computed, ref } from 'vue'
import type { NodeTaskResult, ProbeType } from '@/types/api'
import {
  extractPrimaryLatencyMs,
  formatBytes,
  formatMs,
  latencyColor,
  statusBadgeClass,
  statusLabel
} from '@/utils/format'

const props = defineProps<{
  probeType: ProbeType
  results: NodeTaskResult[]
  ipFilter: string
}>()

const emit = defineEmits<{
  (e: 'clearIpFilter'): void
}>()

const statusTab = ref<'all' | 'ok' | 'fail' | 'overload'>('all')
const keyword = ref('')
const sortKey = ref<'node' | 'latency' | 'loss' | 'status'>('latency')
const sortAsc = ref(true)

function toggleSort(key: 'node' | 'latency' | 'loss' | 'status') {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value
  } else {
    sortKey.value = key
    sortAsc.value = true
  }
}

const tabCounts = computed(() => {
  let ok = 0
  let fail = 0
  let overload = 0
  for (const r of props.results) {
    if (r.status === 'completed') ok++
    else if (r.status === 'timeout' || r.status === 'error') fail++
    else if (r.status === 'overloaded' || r.status === 'expired') overload++
  }
  return { all: props.results.length, ok, fail, overload }
})

const filteredResults = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  const list = props.results.filter((r) => {
    if (props.ipFilter && r.resolved_ip !== props.ipFilter) return false
    if (statusTab.value === 'ok' && r.status !== 'completed') return false
    if (statusTab.value === 'fail' && r.status !== 'timeout' && r.status !== 'error') return false
    if (statusTab.value === 'overload' && r.status !== 'overloaded' && r.status !== 'expired') return false
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
    } else if (sortKey.value === 'status') {
      cmp = a.status.localeCompare(b.status)
    } else if (sortKey.value === 'loss') {
      const la = a.ping?.loss_percent ?? a.tcping?.loss_percent ?? (a.status === 'completed' ? 0 : 100)
      const lb = b.ping?.loss_percent ?? b.tcping?.loss_percent ?? (b.status === 'completed' ? 0 : 100)
      cmp = la - lb
    } else {
      const ma = extractPrimaryLatencyMs(a) ?? Number.MAX_SAFE_INTEGER
      const mb = extractPrimaryLatencyMs(b) ?? Number.MAX_SAFE_INTEGER
      cmp = ma - mb
    }
    return sortAsc.value ? cmp : -cmp
  })
})

function httpCodeClass(code: number): string {
  if (code >= 200 && code < 300) return 'badge-green'
  if (code >= 300 && code < 400) return 'badge-blue'
  if (code >= 400 && code < 500) return 'badge-amber'
  return 'badge-red'
}

function pctWidth(part: number, total: number): string {
  if (!total || total <= 0 || part <= 0) return '0%'
  return `${Math.max(2, (part / total) * 100)}%`
}
</script>

<template>
  <div class="card">
    <div class="card-header table-toolbar">
      <div class="tab-group">
        <button type="button" class="tab-btn" :class="{ active: statusTab === 'all' }" @click="statusTab = 'all'">
          全部 ({{ tabCounts.all }})
        </button>
        <button type="button" class="tab-btn" :class="{ active: statusTab === 'ok' }" @click="statusTab = 'ok'">
          成功 ({{ tabCounts.ok }})
        </button>
        <button type="button" class="tab-btn" :class="{ active: statusTab === 'fail' }" @click="statusTab = 'fail'">
          超时/失败 ({{ tabCounts.fail }})
        </button>
        <button
          v-if="tabCounts.overload > 0"
          type="button"
          class="tab-btn"
          :class="{ active: statusTab === 'overload' }"
          @click="statusTab = 'overload'"
        >
          节点繁忙 ({{ tabCounts.overload }})
        </button>
      </div>

      <div class="search-group">
        <span v-if="ipFilter" class="badge badge-blue">
          IP过滤: {{ ipFilter }}
          <button type="button" class="clear-x" @click="emit('clearIpFilter')">×</button>
        </span>
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
            <th class="sortable" @click="toggleSort('node')">检测节点</th>
            <th class="sortable" @click="toggleSort('status')">状态</th>
            <th>响应 IP / DN11 归属</th>

            <template v-if="probeType === 'ping'">
              <th class="sortable" @click="toggleSort('loss')">丢包率</th>
              <th class="sortable" @click="toggleSort('latency')">平均延迟</th>
              <th>最快 / 最慢 / 抖动</th>
              <th>TTL</th>
              <th>分包明细</th>
            </template>

            <template v-else-if="probeType === 'tcping'">
              <th>端口 / 状态</th>
              <th class="sortable" @click="toggleSort('loss')">丢包率</th>
              <th class="sortable" @click="toggleSort('latency')">平均延迟</th>
              <th>最快 / 最慢</th>
            </template>

            <template v-else-if="probeType === 'http'">
              <th>HTTP 状态</th>
              <th class="sortable" @click="toggleSort('latency')">总耗时</th>
              <th style="min-width: 240px">阶段耗时瀑布 (DNS/TCP/TLS/TTFB/下载)</th>
              <th>大小 / TLS</th>
            </template>

            <template v-else-if="probeType === 'dns'">
              <th>RCODE</th>
              <th class="sortable" @click="toggleSort('latency')">查询耗时</th>
              <th>DNS 服务器</th>
              <th style="min-width: 260px">解析记录</th>
            </template>
          </tr>
        </thead>

        <tbody>
          <tr v-for="row in filteredResults" :key="row.node_id">
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
              <span class="badge" :class="statusBadgeClass(row.status)">
                {{ statusLabel(row.status) }}
              </span>
              <div v-if="row.error_message && row.status !== 'completed'" class="err-hint">
                {{ row.error_message }}
              </div>
            </td>

            <td>
              <div v-if="row.resolved_ip" class="mono ip-val">{{ row.resolved_ip }}</div>
              <div v-if="row.resolved_dn11" class="dn11-tag">
                AS{{ row.resolved_dn11.asn }} · {{ row.resolved_dn11.owner }}
              </div>
              <span v-else-if="!row.resolved_ip" class="text-muted">-</span>
            </td>

            <!-- PING columns -->
            <template v-if="probeType === 'ping'">
              <td class="mono">
                <span v-if="row.ping" :class="{ 'loss-warn': row.ping.loss_percent > 0 }">
                  {{ row.ping.loss_percent.toFixed(1) }}% ({{ row.ping.received }}/{{ row.ping.sent }})
                </span>
                <span v-else>-</span>
              </td>
              <td>
                <span
                  v-if="row.ping && row.ping.received > 0"
                  class="lat-pill"
                  :style="{ background: latencyColor(row.ping.avg_rtt_ms) }"
                >
                  {{ formatMs(row.ping.avg_rtt_ms) }}
                </span>
                <span v-else>-</span>
              </td>
              <td class="mono text-sm">
                <template v-if="row.ping && row.ping.received > 0">
                  {{ formatMs(row.ping.min_rtt_ms) }} / {{ formatMs(row.ping.max_rtt_ms) }} / {{ formatMs(row.ping.mdev_rtt_ms) }}
                </template>
                <span v-else>-</span>
              </td>
              <td class="mono">{{ row.ping?.ttl || '-' }}</td>
              <td>
                <div v-if="row.ping?.packets?.length" class="pkt-strip">
                  <span
                    v-for="pkt in row.ping.packets"
                    :key="pkt.seq"
                    class="pkt-dot"
                    :style="{ background: pkt.timeout ? '#ef4444' : latencyColor(pkt.rtt_ms) }"
                    :title="pkt.timeout ? `#${pkt.seq} 超时` : `#${pkt.seq}: ${pkt.rtt_ms}ms (TTL=${pkt.ttl})`"
                  ></span>
                </div>
                <span v-else>-</span>
              </td>
            </template>

            <!-- TCPING columns -->
            <template v-else-if="probeType === 'tcping'">
              <td class="mono">
                <template v-if="row.tcping">
                  <span class="badge" :class="row.tcping.state === 'open' ? 'badge-green' : 'badge-red'">
                    :{{ row.tcping.port }} {{ row.tcping.state }}
                  </span>
                </template>
                <span v-else>-</span>
              </td>
              <td class="mono">
                <span v-if="row.tcping">
                  {{ row.tcping.loss_percent.toFixed(1) }}% ({{ row.tcping.connected }}/{{ row.tcping.sent }})
                </span>
                <span v-else>-</span>
              </td>
              <td>
                <span
                  v-if="row.tcping && row.tcping.connected > 0"
                  class="lat-pill"
                  :style="{ background: latencyColor(row.tcping.avg_rtt_ms) }"
                >
                  {{ formatMs(row.tcping.avg_rtt_ms) }}
                </span>
                <span v-else>-</span>
              </td>
              <td class="mono text-sm">
                <template v-if="row.tcping && row.tcping.connected > 0">
                  {{ formatMs(row.tcping.min_rtt_ms) }} / {{ formatMs(row.tcping.max_rtt_ms) }}
                </template>
                <span v-else>-</span>
              </td>
            </template>

            <!-- HTTP columns -->
            <template v-else-if="probeType === 'http'">
              <td>
                <span v-if="row.http" class="badge mono" :class="httpCodeClass(row.http.status_code)">
                  {{ row.http.status_code }}
                </span>
                <span v-else>-</span>
              </td>
              <td>
                <span
                  v-if="row.http?.timing"
                  class="lat-pill"
                  :style="{ background: latencyColor(row.http.timing.total_ms) }"
                >
                  {{ formatMs(row.http.timing.total_ms) }}
                </span>
                <span v-else>-</span>
              </td>
              <td>
                <div v-if="row.http?.timing" class="waterfall-wrap">
                  <div
                    class="waterfall-bar"
                    :title="`DNS ${row.http.timing.dns_ms}ms | TCP ${row.http.timing.tcp_ms}ms | TLS ${row.http.timing.tls_ms}ms | TTFB ${row.http.timing.ttfb_ms}ms | 下载 ${row.http.timing.download_ms}ms`"
                  >
                    <span class="wf-dns" :style="{ width: pctWidth(row.http.timing.dns_ms, row.http.timing.total_ms) }"></span>
                    <span class="wf-tcp" :style="{ width: pctWidth(row.http.timing.tcp_ms, row.http.timing.total_ms) }"></span>
                    <span class="wf-tls" :style="{ width: pctWidth(row.http.timing.tls_ms, row.http.timing.total_ms) }"></span>
                    <span class="wf-ttfb" :style="{ width: pctWidth(row.http.timing.ttfb_ms, row.http.timing.total_ms) }"></span>
                    <span class="wf-dl" :style="{ width: pctWidth(row.http.timing.download_ms, row.http.timing.total_ms) }"></span>
                  </div>
                  <div class="wf-nums mono">
                    DNS {{ row.http.timing.dns_ms }} · TCP {{ row.http.timing.tcp_ms }}
                    <template v-if="row.http.timing.tls_ms > 0"> · TLS {{ row.http.timing.tls_ms }}</template>
                    · TTFB {{ row.http.timing.ttfb_ms }}
                  </div>
                </div>
                <span v-else>-</span>
              </td>
              <td class="mono text-sm">
                <template v-if="row.http">
                  <div>{{ formatBytes(row.http.content_length) }}</div>
                  <div v-if="row.http.tls_version" class="text-muted">{{ row.http.tls_version }}</div>
                </template>
                <span v-else>-</span>
              </td>
            </template>

            <!-- DNS columns -->
            <template v-else-if="probeType === 'dns'">
              <td>
                <span v-if="row.dns" class="badge mono" :class="row.dns.rcode === 'NOERROR' ? 'badge-green' : 'badge-amber'">
                  {{ row.dns.rcode || '-' }}
                </span>
                <span v-else>-</span>
              </td>
              <td>
                <span
                  v-if="row.dns"
                  class="lat-pill"
                  :style="{ background: latencyColor(row.dns.query_time_ms) }"
                >
                  {{ formatMs(row.dns.query_time_ms) }}
                </span>
                <span v-else>-</span>
              </td>
              <td class="mono text-sm">{{ row.dns?.server_used ?? '-' }}</td>
              <td>
                <div v-if="row.dns?.records?.length" class="dns-records">
                  <div v-for="(rec, idx) in row.dns.records" :key="idx" class="dns-rec mono">
                    <span class="badge badge-slate">{{ rec.type }}</span>
                    <span>{{ rec.value }}</span>
                    <span class="text-muted">TTL={{ rec.ttl }}</span>
                  </div>
                </div>
                <span v-else-if="row.dns" class="text-muted">无解析记录</span>
                <span v-else>-</span>
              </td>
            </template>
          </tr>

          <tr v-if="filteredResults.length === 0">
            <td colspan="8" class="empty-row">暂无匹配的探测结果</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.table-toolbar {
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

.search-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.search-input {
  border: 1px solid var(--border-strong);
  border-radius: 5px;
  padding: 5px 10px;
  font-size: 12px;
  width: 210px;
  outline: none;
}

.search-input:focus {
  border-color: var(--brand-blue);
}

.clear-x {
  border: none;
  background: none;
  color: inherit;
  cursor: pointer;
  font-weight: 700;
  padding: 0 2px;
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
  color: var(--text-primary);
}

.node-sub {
  font-size: 11px;
  color: var(--text-muted);
}

.err-hint {
  font-size: 11px;
  color: #b91c1c;
  max-width: 190px;
  margin-top: 2px;
}

.ip-val {
  font-weight: 600;
  color: var(--text-primary);
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

.pkt-strip {
  display: flex;
  gap: 4px;
  align-items: center;
}

.pkt-dot {
  width: 10px;
  height: 10px;
  border-radius: 2px;
  display: inline-block;
}

.waterfall-wrap {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.waterfall-bar {
  display: flex;
  height: 10px;
  background: #f1f5f9;
  border-radius: 3px;
  overflow: hidden;
}

.wf-dns {
  background: #0ea5e9;
}
.wf-tcp {
  background: #22c55e;
}
.wf-tls {
  background: #a855f7;
}
.wf-ttfb {
  background: #f59e0b;
}
.wf-dl {
  background: #3b82f6;
}

.wf-nums {
  font-size: 11px;
  color: var(--text-muted);
}

.dns-records {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.dns-rec {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}

.empty-row {
  text-align: center;
  padding: 28px;
  color: var(--text-muted);
}
</style>
