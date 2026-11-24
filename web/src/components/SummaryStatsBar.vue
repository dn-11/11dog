<script setup lang="ts">
import { computed } from 'vue'
import type { DN11Match, NodeTaskResult, TaskSnapshot } from '@/types/api'
import { extractPrimaryLatencyMs, formatMs, latencyColor } from '@/utils/format'

const props = defineProps<{
  snapshot: TaskSnapshot
  selectedIPFilter: string
}>()

const emit = defineEmits<{
  (e: 'update:selectedIPFilter', ip: string): void
  (e: 'exportCsv'): void
  (e: 'exportJson'): void
}>()

const results = computed(() => Object.values(props.snapshot.results))

const progressPct = computed(() => {
  if (!props.snapshot.total_nodes) return 0
  return Math.round((props.snapshot.completed_nodes / props.snapshot.total_nodes) * 100)
})

const stats = computed(() => {
  let fastest: { ms: number; node: string } | null = null
  let slowest: { ms: number; node: string } | null = null
  let sumMs = 0
  let countMs = 0
  let lossSum = 0
  let lossCount = 0

  for (const r of results.value) {
    const ms = extractPrimaryLatencyMs(r)
    if (ms !== null) {
      sumMs += ms
      countMs++
      if (!fastest || ms < fastest.ms) fastest = { ms, node: r.node_name }
      if (!slowest || ms > slowest.ms) slowest = { ms, node: r.node_name }
    }
    if (r.ping && r.ping.sent > 0) {
      lossSum += r.ping.loss_percent
      lossCount++
    } else if (r.tcping && r.tcping.sent > 0) {
      lossSum += r.tcping.loss_percent
      lossCount++
    } else if (r.status === 'timeout' || r.status === 'error') {
      lossSum += 100
      lossCount++
    } else if (r.status === 'completed') {
      lossCount++
    }
  }

  return {
    fastest,
    slowest,
    avgMs: countMs > 0 ? sumMs / countMs : null,
    avgLoss: lossCount > 0 ? lossSum / lossCount : null
  }
})

interface ResolvedIPGroup {
  ip: string
  count: number
  dn11?: DN11Match
}

const resolvedIPs = computed<ResolvedIPGroup[]>(() => {
  const map = new Map<string, ResolvedIPGroup>()
  for (const r of results.value) {
    if (!r.resolved_ip) continue
    const existing = map.get(r.resolved_ip)
    if (existing) {
      existing.count++
      if (!existing.dn11 && r.resolved_dn11) existing.dn11 = r.resolved_dn11
    } else {
      map.set(r.resolved_ip, { ip: r.resolved_ip, count: 1, dn11: r.resolved_dn11 })
    }
  }
  return Array.from(map.values()).sort((a, b) => b.count - a.count)
})

function toggleIP(ip: string) {
  emit('update:selectedIPFilter', props.selectedIPFilter === ip ? '' : ip)
}
</script>

<template>
  <div class="card">
    <div class="card-body">
      <div class="progress-header">
        <div class="progress-title">
          <span>探测进度：</span>
          <strong class="mono">{{ snapshot.completed_nodes }} / {{ snapshot.total_nodes }}</strong>
          <span class="progress-target mono">{{ snapshot.target }}</span>
          <span v-if="snapshot.overloaded_nodes > 0" class="badge badge-purple">
            {{ snapshot.overloaded_nodes }} 个监测点繁忙
          </span>
        </div>
        <div class="export-actions">
          <button type="button" class="btn btn-outline btn-sm" @click="emit('exportCsv')">
            导出 CSV
          </button>
          <button type="button" class="btn btn-outline btn-sm" @click="emit('exportJson')">
            导出 JSON
          </button>
        </div>
      </div>

      <div class="progress-track">
        <div class="progress-fill" :style="{ width: `${progressPct}%` }"></div>
      </div>

      <div class="kpi-grid">
        <div class="kpi-box">
          <div class="kpi-label">最快节点</div>
          <div class="kpi-val mono" :style="{ color: latencyColor(stats.fastest?.ms) }">
            {{ stats.fastest ? formatMs(stats.fastest.ms) : '-' }}
          </div>
          <div class="kpi-sub">{{ stats.fastest?.node ?? '等待结果' }}</div>
        </div>
        <div class="kpi-box">
          <div class="kpi-label">最慢节点</div>
          <div class="kpi-val mono" :style="{ color: latencyColor(stats.slowest?.ms) }">
            {{ stats.slowest ? formatMs(stats.slowest.ms) : '-' }}
          </div>
          <div class="kpi-sub">{{ stats.slowest?.node ?? '等待结果' }}</div>
        </div>
        <div class="kpi-box">
          <div class="kpi-label">平均延迟</div>
          <div class="kpi-val mono" :style="{ color: latencyColor(stats.avgMs) }">
            {{ formatMs(stats.avgMs) }}
          </div>
          <div class="kpi-sub">有效节点均值</div>
        </div>
        <div class="kpi-box">
          <div class="kpi-label">平均丢包率</div>
          <div class="kpi-val mono">
            {{ stats.avgLoss !== null ? `${stats.avgLoss.toFixed(1)}%` : '-' }}
          </div>
          <div class="kpi-sub">综合可用性</div>
        </div>
        <div class="kpi-box ip-box">
          <div class="kpi-label">解析 IP ({{ resolvedIPs.length }})</div>
          <div class="ip-pills">
            <button
              v-for="item in resolvedIPs"
              :key="item.ip"
              type="button"
              class="ip-pill mono"
              :class="{ active: selectedIPFilter === item.ip }"
              @click="toggleIP(item.ip)"
              :title="item.dn11 ? `AS${item.dn11.asn} · ${item.dn11.owner}` : '点击按此 IP 过滤结果'"
            >
              <span>{{ item.ip }}</span>
              <span v-if="item.dn11" class="ip-asn">AS{{ item.dn11.asn }}</span>
              <span class="ip-cnt">×{{ item.count }}</span>
            </button>
            <span v-if="resolvedIPs.length === 0" class="kpi-sub">-</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.progress-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}

.progress-title {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 13px;
}

.progress-target {
  background: #f1f5f9;
  color: var(--text-secondary);
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.export-actions {
  display: flex;
  gap: 6px;
}

.progress-track {
  height: 6px;
  background: #e2e8f0;
  border-radius: 999px;
  overflow: hidden;
  margin-bottom: 14px;
}

.progress-fill {
  height: 100%;
  background: linear-gradient(90deg, #1677ff, #22c55e);
  transition: width 0.2s ease;
}

.kpi-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(130px, 1fr)) minmax(240px, 1.6fr);
  gap: 10px;
}

.kpi-box {
  background: #f8fafc;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  padding: 10px 12px;
}

.kpi-label {
  font-size: 12px;
  color: var(--text-secondary);
  margin-bottom: 2px;
}

.kpi-val {
  font-size: 19px;
  font-weight: 700;
  line-height: 1.25;
}

.kpi-sub {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ip-pills {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 4px;
}

.ip-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 8px;
  border-radius: 4px;
  border: 1px solid var(--border-strong);
  background: #fff;
  font-size: 12px;
  cursor: pointer;
}

.ip-pill:hover,
.ip-pill.active {
  border-color: var(--brand-blue);
  background: #eff6ff;
  color: #1d4ed8;
}

.ip-asn {
  background: #dbeafe;
  color: #1e40af;
  padding: 0 4px;
  border-radius: 3px;
  font-size: 11px;
}

.ip-cnt {
  color: var(--text-muted);
  font-size: 11px;
}

@media (max-width: 960px) {
  .kpi-grid {
    grid-template-columns: repeat(2, 1fr);
  }
  .ip-box {
    grid-column: 1 / -1;
  }
}
</style>
