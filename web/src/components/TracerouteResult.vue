<script setup lang="ts">
import { ref } from 'vue'
import type { NodeTaskResult } from '@/types/api'
import { formatMs, latencyColor, statusBadgeClass, statusLabel } from '@/utils/format'

defineProps<{
  results: NodeTaskResult[]
}>()

const collapsed = ref<Record<string, boolean>>({})

function toggle(nodeID: string) {
  collapsed.value[nodeID] = !collapsed.value[nodeID]
}
</script>

<template>
  <div class="trace-list">
    <div v-for="res in results" :key="res.node_id" class="card">
      <div class="card-header trace-head" @click="toggle(res.node_id)">
        <div class="trace-head-left">
          <span class="region-tag">{{ res.region }}</span>
          <strong>{{ res.node_name }}</strong>
          <span class="mono text-muted">{{ res.group }}</span>
          <span class="badge" :class="statusBadgeClass(res.status)">{{ statusLabel(res.status) }}</span>
          <span v-if="res.resolved_ip" class="mono ip-chip">
            目标 IP: {{ res.resolved_ip }}
            <template v-if="res.resolved_dn11"> (AS{{ res.resolved_dn11.asn }} {{ res.resolved_dn11.owner }})</template>
          </span>
          <span v-if="res.traceroute?.reached" class="badge badge-green">
            已抵达目标 ({{ res.traceroute.hops.length }} 跳)
          </span>
        </div>
        <button type="button" class="btn btn-outline btn-sm">
          {{ collapsed[res.node_id] ? '展开路由' : '收起' }}
        </button>
      </div>

      <div v-if="!collapsed[res.node_id]" class="data-table-wrap">
        <div v-if="res.error_message && res.status !== 'completed'" class="err-box">
          {{ res.error_message }}
        </div>
        <table v-if="res.traceroute?.hops?.length" class="data-table">
          <thead>
            <tr>
              <th style="width: 60px">跳数</th>
              <th>路由器 IP</th>
              <th>DN11 ASN / 归属</th>
              <th>PTR 主机名</th>
              <th>探测包 RTT (ms)</th>
              <th>平均延迟</th>
              <th style="width: 90px">丢包率</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="hop in res.traceroute.hops" :key="hop.hop">
              <td class="mono"><strong>#{{ hop.hop }}</strong></td>
              <td class="mono">
                <span v-if="!hop.timeout && hop.ip">{{ hop.ip }}</span>
                <span v-else class="text-muted">* * *</span>
              </td>
              <td>
                <span v-if="hop.dn11" class="badge badge-blue mono">
                  AS{{ hop.dn11.asn }} · {{ hop.dn11.owner }}
                </span>
                <span v-else class="text-muted">-</span>
              </td>
              <td class="mono text-sm">{{ hop.hostname || '-' }}</td>
              <td class="mono text-sm">
                <template v-if="hop.rtts_ms?.length">
                  {{ hop.rtts_ms.map((v) => v.toFixed(1)).join(' / ') }}
                </template>
                <span v-else class="text-muted">*</span>
              </td>
              <td>
                <span
                  v-if="!hop.timeout && hop.avg_rtt_ms > 0"
                  class="lat-pill"
                  :style="{ background: latencyColor(hop.avg_rtt_ms) }"
                >
                  {{ formatMs(hop.avg_rtt_ms) }}
                </span>
                <span v-else class="text-muted">超时</span>
              </td>
              <td class="mono">{{ hop.loss_percent.toFixed(0) }}%</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty-hops">
          {{ res.status === 'running' || res.status === 'queued' ? '正在逐跳追踪路由...' : '无可用路由跳数记录' }}
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.trace-list {
  display: flex;
  flex-direction: column;
}

.trace-head {
  cursor: pointer;
}

.trace-head-left {
  display: flex;
  flex-wrap: wrap;
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
}

.ip-chip {
  font-size: 12px;
  color: var(--text-secondary);
}

.text-muted {
  color: var(--text-muted);
  font-size: 12px;
}

.text-sm {
  font-size: 12px;
}

.err-box {
  padding: 10px 18px;
  color: #b91c1c;
  background: #fef2f2;
  font-size: 13px;
}

.empty-hops {
  padding: 18px;
  text-align: center;
  color: var(--text-muted);
  font-size: 13px;
}
</style>
