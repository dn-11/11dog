<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ContinuousNodeStats, ContinuousSession } from '@/composables/useProbeTask'
import { formatMs, latencyColor } from '@/utils/format'

const props = defineProps<{
  session: ContinuousSession
  checkedChartIDs: string[]
}>()

const emit = defineEmits<{
  (e: 'update:checkedChartIDs', ids: string[]): void
  (e: 'pause'): void
  (e: 'resume'): void
  (e: 'clear'): void
  (e: 'exportCsv'): void
  (e: 'exportJson'): void
}>()

const PALETTE = [
  '#1677ff',
  '#16a34a',
  '#ea580c',
  '#9333ea',
  '#0891b2',
  '#db2777',
  '#ca8a04',
  '#4f46e5',
  '#0d9488',
  '#dc2626'
]

const showGlobalAvg = ref(true)
const hoverIdx = ref<number | null>(null)
const svgRef = ref<SVGSVGElement | null>(null)

const W = 960
const H = 250
const PAD = { top: 18, right: 20, bottom: 28, left: 52 }
const PLOT_W = W - PAD.left - PAD.right
const PLOT_H = H - PAD.top - PAD.bottom

const allNodes = computed(() => Object.values(props.session.nodes))

const plottedNodes = computed<ContinuousNodeStats[]>(() => {
  const checkedSet = new Set(props.checkedChartIDs)
  return allNodes.value.filter((n) => checkedSet.has(n.node_id))
})

function nodeColor(nodeID: string): string {
  const idx = allNodes.value.findIndex((n) => n.node_id === nodeID)
  return PALETTE[(idx >= 0 ? idx : 0) % PALETTE.length]
}

const ticks = computed(() => props.session.timestamps)

// Map each node's samples by round number for O(1) alignment to timestamps
const nodeSampleMaps = computed(() => {
  const result = new Map<string, Map<number, { ms: number | null; status: string }>>()
  for (const n of allNodes.value) {
    const m = new Map<number, { ms: number | null; status: string }>()
    for (const s of n.samples) {
      m.set(s.round, { ms: s.ms, status: s.status })
    }
    result.set(n.node_id, m)
  }
  return result
})

const globalAvgSeries = computed<(number | null)[]>(() => {
  return ticks.value.map((t) => {
    let sum = 0
    let cnt = 0
    for (const n of allNodes.value) {
      const pt = nodeSampleMaps.value.get(n.node_id)?.get(t.round)
      if (pt && pt.ms !== null) {
        sum += pt.ms
        cnt++
      }
    }
    return cnt > 0 ? Math.round((sum / cnt) * 100) / 100 : null
  })
})

const summaryKPI = computed(() => {
  const validLatest = allNodes.value.map((n) => n.last_ms).filter((v): v is number => v !== null)
  const latestAvg = validLatest.length > 0 ? validLatest.reduce((a, b) => a + b, 0) / validLatest.length : null

  let sumSent = 0
  let sumLost = 0
  let sumAvg = 0
  let cntAvg = 0
  for (const n of allNodes.value) {
    sumSent += n.sent
    sumLost += n.lost
    if (n.avg_ms !== null) {
      sumAvg += n.avg_ms
      cntAvg++
    }
  }
  return {
    latestAvg,
    overallAvg: cntAvg > 0 ? sumAvg / cntAvg : null,
    lossPct: sumSent > 0 ? (sumLost / sumSent) * 100 : 0
  }
})

const yMax = computed(() => {
  let maxVal = 20
  for (const n of plottedNodes.value) {
    const m = nodeSampleMaps.value.get(n.node_id)
    if (!m) continue
    for (const t of ticks.value) {
      const pt = m.get(t.round)
      if (pt?.ms !== null && pt?.ms !== undefined && pt.ms > maxVal) {
        maxVal = pt.ms
      }
    }
  }
  if (showGlobalAvg.value) {
    for (const v of globalAvgSeries.value) {
      if (v !== null && v > maxVal) maxVal = v
    }
  }
  const padded = maxVal * 1.15
  if (padded <= 20) return 20
  if (padded <= 50) return 50
  if (padded <= 100) return 100
  if (padded <= 150) return 150
  if (padded <= 200) return 200
  if (padded <= 300) return 300
  if (padded <= 500) return 500
  return Math.ceil(padded / 100) * 100
})

const yTicks = computed(() => {
  const m = yMax.value
  return [0, 0.25, 0.5, 0.75, 1].map((ratio) => ({
    val: Math.round(m * ratio),
    y: PAD.top + PLOT_H * (1 - ratio)
  }))
})

function xForIndex(idx: number): number {
  const total = ticks.value.length
  if (total <= 1) return PAD.left + PLOT_W / 2
  return PAD.left + (idx / (total - 1)) * PLOT_W
}

function yForMs(ms: number): number {
  const clamped = Math.max(0, Math.min(ms, yMax.value))
  return PAD.top + PLOT_H * (1 - clamped / yMax.value)
}

function buildPolylineSegments(values: (number | null)[]): string {
  const parts: string[] = []
  let drawing = false
  for (let i = 0; i < values.length; i++) {
    const v = values[i]
    if (v === null || v === undefined) {
      drawing = false
      continue
    }
    const x = xForIndex(i).toFixed(1)
    const y = yForMs(v).toFixed(1)
    parts.push(`${drawing ? 'L' : 'M'}${x},${y}`)
    drawing = true
  }
  return parts.join(' ')
}

const plottedPaths = computed(() => {
  return plottedNodes.value.map((n) => {
    const m = nodeSampleMaps.value.get(n.node_id)
    const vals: (number | null)[] = []
    const drops: { x: number; y: number; round: number }[] = []
    ticks.value.forEach((t, idx) => {
      const pt = m?.get(t.round)
      if (!pt) {
        vals.push(null)
      } else if (pt.ms !== null) {
        vals.push(pt.ms)
      } else {
        vals.push(null)
        drops.push({ x: xForIndex(idx), y: PAD.top + PLOT_H - 4, round: t.round })
      }
    })
    return {
      node: n,
      color: nodeColor(n.node_id),
      d: buildPolylineSegments(vals),
      drops
    }
  })
})

const globalAvgPath = computed(() => buildPolylineSegments(globalAvgSeries.value))

const xLabels = computed(() => {
  const total = ticks.value.length
  if (total === 0) return []
  const step = Math.max(1, Math.floor(total / 6))
  const labels: { x: number; text: string }[] = []
  for (let i = 0; i < total; i += step) {
    labels.push({ x: xForIndex(i), text: formatTimeShort(ticks.value[i].ts) })
  }
  const lastIdx = total - 1
  if (lastIdx > 0 && (labels.length === 0 || Math.abs(labels[labels.length - 1].x - xForIndex(lastIdx)) > 48)) {
    labels.push({ x: xForIndex(lastIdx), text: formatTimeShort(ticks.value[lastIdx].ts) })
  }
  return labels
})

function formatTimeShort(ts: number): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function formatDuration(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}

function handleMouseMove(e: MouseEvent) {
  if (!svgRef.value || ticks.value.length === 0) return
  const rect = svgRef.value.getBoundingClientRect()
  if (rect.width <= 0) return
  const relX = ((e.clientX - rect.left) / rect.width) * W
  if (relX < PAD.left || relX > W - PAD.right) {
    hoverIdx.value = null
    return
  }
  if (ticks.value.length === 1) {
    hoverIdx.value = 0
    return
  }
  const ratio = (relX - PAD.left) / PLOT_W
  hoverIdx.value = Math.max(0, Math.min(ticks.value.length - 1, Math.round(ratio * (ticks.value.length - 1))))
}

const hoverInfo = computed(() => {
  if (hoverIdx.value === null) return null
  const t = ticks.value[hoverIdx.value]
  if (!t) return null
  const x = xForIndex(hoverIdx.value)
  const avg = globalAvgSeries.value[hoverIdx.value]
  const items = plottedNodes.value.map((n) => {
    const pt = nodeSampleMaps.value.get(n.node_id)?.get(t.round)
    return {
      node_id: n.node_id,
      name: n.node_name,
      color: nodeColor(n.node_id),
      ms: pt?.ms ?? null,
      status: pt?.status ?? 'queued'
    }
  })
  return {
    round: t.round,
    time: formatTimeShort(t.ts),
    x,
    xPct: (x / W) * 100,
    avg,
    items
  }
})

function toggleLegend(nodeID: string) {
  const set = new Set(props.checkedChartIDs)
  if (set.has(nodeID)) {
    set.delete(nodeID)
  } else {
    set.add(nodeID)
  }
  emit('update:checkedChartIDs', Array.from(set))
}
</script>

<template>
  <div class="card">
    <div class="card-header cont-header">
      <div class="cont-title-group">
        <span
          class="status-dot"
          :class="{
            running: session.active && !session.paused,
            paused: session.active && session.paused,
            stopped: !session.active
          }"
        ></span>
        <strong>
          {{
            session.active
              ? session.paused
                ? '已暂停持续测试'
                : '实时持续测试中'
              : '持续测试已停止'
          }}
        </strong>
        <span class="badge badge-blue mono">
          第 {{ session.round }}<template v-if="session.maxRounds > 0">/{{ session.maxRounds }}</template> 轮
        </span>
        <span class="badge badge-slate mono">间隔 {{ session.intervalSec }}s</span>
        <span class="badge badge-slate mono">耗时 {{ formatDuration(session.elapsedSec) }}</span>
        <span class="target-tag mono">{{ session.target }}</span>
      </div>

      <div class="cont-controls">
        <label class="avg-toggle">
          <input v-model="showGlobalAvg" type="checkbox" />
          <span>全网均值线</span>
        </label>
        <button
          v-if="session.active && !session.paused"
          type="button"
          class="btn btn-outline btn-sm"
          @click="emit('pause')"
        >
          暂停
        </button>
        <button
          v-else-if="session.active && session.paused"
          type="button"
          class="btn btn-success btn-sm"
          @click="emit('resume')"
        >
          继续
        </button>
        <button type="button" class="btn btn-outline btn-sm" @click="emit('clear')">清空数据</button>
        <button type="button" class="btn btn-outline btn-sm" @click="emit('exportCsv')">导出 CSV</button>
        <button type="button" class="btn btn-outline btn-sm" @click="emit('exportJson')">导出 JSON</button>
      </div>
    </div>

    <div class="card-body">
      <div class="mini-kpi-strip">
        <div class="mini-kpi">
          <span class="mk-label">当前全网均值</span>
          <span class="mk-val mono" :style="{ color: latencyColor(summaryKPI.latestAvg) }">
            {{ formatMs(summaryKPI.latestAvg) }}
          </span>
        </div>
        <div class="mini-kpi">
          <span class="mk-label">累计全网均值</span>
          <span class="mk-val mono" :style="{ color: latencyColor(summaryKPI.overallAvg) }">
            {{ formatMs(summaryKPI.overallAvg) }}
          </span>
        </div>
        <div class="mini-kpi">
          <span class="mk-label">综合丢包率</span>
          <span class="mk-val mono" :style="{ color: summaryKPI.lossPct > 0 ? '#dc2626' : '#15803d' }">
            {{ summaryKPI.lossPct.toFixed(1) }}%
          </span>
        </div>
        <div class="mini-kpi">
          <span class="mk-label">曲线展示</span>
          <span class="mk-val mono">{{ plottedNodes.length }} / {{ allNodes.length }} 节点</span>
        </div>
      </div>

      <div class="chart-stage" @mouseleave="hoverIdx = null">
        <svg
          ref="svgRef"
          :viewBox="`0 0 ${W} ${H}`"
          class="chart-svg"
          @mousemove="handleMouseMove"
        >
          <!-- Horizontal Grid Lines -->
          <g v-for="yt in yTicks" :key="yt.val">
            <line
              :x1="PAD.left"
              :y1="yt.y"
              :x2="W - PAD.right"
              :y2="yt.y"
              stroke="#e2e8f0"
              stroke-dasharray="3 3"
              stroke-width="1"
            />
            <text
              :x="PAD.left - 8"
              :y="yt.y + 4"
              text-anchor="end"
              fill="#64748b"
              font-size="11"
              class="mono"
            >
              {{ yt.val }}ms
            </text>
          </g>

          <!-- X Axis Timestamps -->
          <g v-for="xl in xLabels" :key="xl.x + xl.text">
            <text
              :x="xl.x"
              :y="H - 6"
              text-anchor="middle"
              fill="#64748b"
              font-size="11"
              class="mono"
            >
              {{ xl.text }}
            </text>
          </g>

          <!-- Per-node polylines & drop markers -->
          <g v-for="line in plottedPaths" :key="line.node.node_id">
            <path
              v-if="line.d"
              :d="line.d"
              fill="none"
              :stroke="line.color"
              stroke-width="2"
              stroke-linejoin="round"
              stroke-linecap="round"
            />
            <circle
              v-for="drop in line.drops"
              :key="drop.round"
              :cx="drop.x"
              :cy="drop.y"
              r="3.2"
              fill="#ef4444"
              stroke="#fff"
              stroke-width="1"
            />
          </g>

          <!-- Global Average dashed line -->
          <path
            v-if="showGlobalAvg && globalAvgPath"
            :d="globalAvgPath"
            fill="none"
            stroke="#0f172a"
            stroke-width="2.2"
            stroke-dasharray="6 4"
            stroke-linejoin="round"
          />

          <!-- Hover Crosshair & Active Dots -->
          <g v-if="hoverInfo">
            <line
              :x1="hoverInfo.x"
              :y1="PAD.top"
              :x2="hoverInfo.x"
              :y2="PAD.top + PLOT_H"
              stroke="#94a3b8"
              stroke-width="1"
              stroke-dasharray="3 3"
            />
            <circle
              v-if="showGlobalAvg && hoverInfo.avg !== null"
              :cx="hoverInfo.x"
              :cy="yForMs(hoverInfo.avg)"
              r="4"
              fill="#0f172a"
              stroke="#fff"
              stroke-width="1.5"
            />
            <template v-for="item in hoverInfo.items" :key="item.node_id">
              <circle
                v-if="item.ms !== null"
                :cx="hoverInfo.x"
                :cy="yForMs(item.ms)"
                r="4"
                :fill="item.color"
                stroke="#fff"
                stroke-width="1.5"
              />
            </template>
          </g>
        </svg>

        <!-- Floating Tooltip -->
        <div
          v-if="hoverInfo"
          class="chart-tooltip"
          :style="{
            left: hoverInfo.xPct > 65 ? undefined : `${Math.min(72, hoverInfo.xPct + 2)}%`,
            right: hoverInfo.xPct > 65 ? `${Math.max(4, 100 - hoverInfo.xPct + 2)}%` : undefined
          }"
        >
          <div class="tt-head mono">#{{ hoverInfo.round }} · {{ hoverInfo.time }}</div>
          <div v-if="showGlobalAvg" class="tt-row">
            <span class="tt-dot" style="background: #0f172a"></span>
            <span class="tt-name">全网均值</span>
            <span class="tt-val mono">{{ formatMs(hoverInfo.avg) }}</span>
          </div>
          <div v-for="item in hoverInfo.items" :key="item.node_id" class="tt-row">
            <span class="tt-dot" :style="{ background: item.color }"></span>
            <span class="tt-name">{{ item.name }}</span>
            <span
              class="tt-val mono"
              :style="{ color: item.ms === null ? '#ef4444' : '#f8fafc' }"
            >
              {{ item.ms !== null ? formatMs(item.ms) : item.status === 'overloaded' ? '繁忙' : '丢包' }}
            </span>
          </div>
        </div>

        <div v-if="ticks.length === 0" class="chart-empty">正在发送首轮探测包...</div>
      </div>

      <!-- Interactive Legend Strip -->
      <div v-if="allNodes.length > 0" class="legend-strip">
        <button
          v-for="n in allNodes"
          :key="n.node_id"
          type="button"
          class="legend-pill"
          :class="{ active: checkedChartIDs.includes(n.node_id) }"
          @click="toggleLegend(n.node_id)"
        >
          <span
            class="legend-swatch"
            :style="{
              background: checkedChartIDs.includes(n.node_id) ? nodeColor(n.node_id) : '#cbd5e1'
            }"
          ></span>
          <span>{{ n.node_name }}</span>
          <span class="mono legend-ms">
            {{ n.last_ms !== null ? formatMs(n.last_ms) : n.sent > 0 ? '丢包' : '-' }}
          </span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cont-header {
  flex-wrap: wrap;
  gap: 10px;
}

.cont-title-group {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.status-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  display: inline-block;
}

.status-dot.running {
  background: #22c55e;
  box-shadow: 0 0 0 3px rgba(34, 197, 94, 0.25);
  animation: pulse 1.4s infinite;
}

.status-dot.paused {
  background: #f59e0b;
}

.status-dot.stopped {
  background: #64748b;
}

@keyframes pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.45;
  }
}

.target-tag {
  background: #f1f5f9;
  color: var(--text-secondary);
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.cont-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.avg-toggle {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
}

.mini-kpi-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-bottom: 12px;
  padding: 8px 12px;
  background: #f8fafc;
  border: 1px solid var(--border-color);
  border-radius: 6px;
}

.mini-kpi {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.mk-label {
  font-size: 12px;
  color: var(--text-secondary);
}

.mk-val {
  font-size: 15px;
  font-weight: 700;
}

.chart-stage {
  position: relative;
  background: #ffffff;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  padding: 6px 4px 2px;
}

.chart-svg {
  width: 100%;
  height: 250px;
  display: block;
}

.chart-empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 13px;
  pointer-events: none;
}

.chart-tooltip {
  position: absolute;
  top: 10px;
  background: rgba(15, 23, 42, 0.92);
  color: #f8fafc;
  border-radius: 6px;
  padding: 8px 10px;
  font-size: 12px;
  min-width: 175px;
  max-width: 260px;
  pointer-events: none;
  box-shadow: 0 4px 12px rgba(15, 23, 42, 0.25);
  z-index: 5;
}

.tt-head {
  font-weight: 700;
  color: #93c5fd;
  margin-bottom: 5px;
  padding-bottom: 4px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.14);
}

.tt-row {
  display: flex;
  align-items: center;
  gap: 6px;
  line-height: 1.5;
}

.tt-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.tt-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tt-val {
  font-weight: 700;
}

.legend-strip {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.legend-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 9px;
  border-radius: 999px;
  border: 1px solid var(--border-color);
  background: #f8fafc;
  color: var(--text-secondary);
  font-size: 12px;
  cursor: pointer;
  opacity: 0.7;
}

.legend-pill.active {
  background: #fff;
  border-color: var(--border-strong);
  color: var(--text-primary);
  opacity: 1;
  font-weight: 600;
}

.legend-swatch {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.legend-ms {
  font-size: 11px;
  color: var(--text-muted);
}
</style>
