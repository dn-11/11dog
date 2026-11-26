<script setup lang="ts">
import { computed } from 'vue'
import type { NodeTaskResult } from '@/types/api'
import { extractPrimaryLatencyMs, formatMs, latencyColor } from '@/utils/format'

const props = defineProps<{
  results: NodeTaskResult[]
}>()

interface RegionSummary {
  region: string
  total: number
  okCount: number
  avgMs: number | null
}

const regionHeatmap = computed<RegionSummary[]>(() => {
  const map = new Map<string, { total: number; latencies: number[] }>()
  for (const r of props.results) {
    const key = r.region || '未知区域'
    let entry = map.get(key)
    if (!entry) {
      entry = { total: 0, latencies: [] }
      map.set(key, entry)
    }
    entry.total++
    const ms = extractPrimaryLatencyMs(r)
    if (ms !== null) entry.latencies.push(ms)
  }
  return Array.from(map.entries())
    .map(([region, v]) => ({
      region,
      total: v.total,
      okCount: v.latencies.length,
      avgMs: v.latencies.length > 0 ? v.latencies.reduce((a, b) => a + b, 0) / v.latencies.length : null
    }))
    .sort((a, b) => {
      if (a.avgMs === null && b.avgMs === null) return a.region.localeCompare(b.region)
      if (a.avgMs === null) return 1
      if (b.avgMs === null) return -1
      return a.avgMs - b.avgMs
    })
})

const buckets = computed(() => {
  const counts = [
    { label: '<30ms', color: '#15803d', count: 0 },
    { label: '30-80ms', color: '#22c55e', count: 0 },
    { label: '80-150ms', color: '#eab308', count: 0 },
    { label: '150-250ms', color: '#f97316', count: 0 },
    { label: '>250ms', color: '#ef4444', count: 0 },
    { label: '超时/异常', color: '#64748b', count: 0 }
  ]
  for (const r of props.results) {
    const ms = extractPrimaryLatencyMs(r)
    if (ms === null) {
      if (r.status !== 'queued' && r.status !== 'running') {
        counts[5].count++
      }
      continue
    }
    if (ms < 30) counts[0].count++
    else if (ms < 80) counts[1].count++
    else if (ms < 150) counts[2].count++
    else if (ms < 250) counts[3].count++
    else counts[4].count++
  }
  const max = Math.max(1, ...counts.map((b) => b.count))
  return counts.map((b) => ({
    ...b,
    pct: Math.round((b.count / max) * 100)
  }))
})
</script>

<template>
  <div class="visual-grid">
    <div class="card">
      <div class="card-header">
        <span>DN11 区域延迟热力图</span>
        <div class="legend-row">
          <span class="leg"><i style="background:#15803d"></i>&lt;30ms</span>
          <span class="leg"><i style="background:#22c55e"></i>30-80ms</span>
          <span class="leg"><i style="background:#eab308"></i>80-150ms</span>
          <span class="leg"><i style="background:#f97316"></i>150-250ms</span>
          <span class="leg"><i style="background:#ef4444"></i>&gt;250ms</span>
          <span class="leg"><i style="background:#64748b"></i>异常</span>
        </div>
      </div>
      <div class="card-body">
        <div class="region-tiles">
          <div
            v-for="item in regionHeatmap"
            :key="item.region"
            class="region-tile"
            :style="{ borderTopColor: latencyColor(item.avgMs) }"
          >
            <div class="region-name">{{ item.region }}</div>
            <div class="region-rtt mono" :style="{ color: latencyColor(item.avgMs) }">
              {{ item.avgMs !== null ? formatMs(item.avgMs) : '异常/等待' }}
            </div>
            <div class="region-sub">{{ item.okCount }}/{{ item.total }} 节点可达</div>
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <span>延迟分段分布</span>
      </div>
      <div class="card-body">
        <div class="buckets-list">
          <div v-for="b in buckets" :key="b.label" class="bucket-row">
            <span class="bucket-label">{{ b.label }}</span>
            <div class="bucket-track">
              <div class="bucket-bar" :style="{ width: `${b.pct}%`, background: b.color }"></div>
            </div>
            <span class="bucket-count mono">{{ b.count }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.visual-grid {
  display: grid;
  grid-template-columns: 1.35fr 1fr;
  gap: 14px;
}

.legend-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 11px;
  font-weight: 500;
  color: var(--text-secondary);
}

.leg {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.leg i {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 2px;
}

.region-tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(125px, 1fr));
  gap: 8px;
}

.region-tile {
  background: #f8fafc;
  border: 1px solid var(--border-color);
  border-top-width: 4px;
  border-radius: 6px;
  padding: 8px 10px;
}

.region-name {
  font-weight: 600;
  font-size: 13px;
  color: var(--text-primary);
}

.region-rtt {
  font-weight: 700;
  font-size: 15px;
  margin: 2px 0;
}

.region-sub {
  font-size: 11px;
  color: var(--text-muted);
}

.buckets-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.bucket-row {
  display: grid;
  grid-template-columns: 78px 1fr 32px;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.bucket-label {
  color: var(--text-secondary);
}

.bucket-track {
  height: 12px;
  background: #f1f5f9;
  border-radius: 4px;
  overflow: hidden;
}

.bucket-bar {
  height: 100%;
  border-radius: 4px;
  transition: width 0.25s ease;
}

.bucket-count {
  text-align: right;
  font-weight: 600;
  color: var(--text-primary);
}

@media (max-width: 960px) {
  .visual-grid {
    grid-template-columns: 1fr;
  }
}
</style>
