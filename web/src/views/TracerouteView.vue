<script setup lang="ts">
import { ref, watch } from 'vue'
import NodeFilterBar from '@/components/NodeFilterBar.vue'
import SummaryStatsBar from '@/components/SummaryStatsBar.vue'
import TracerouteResult from '@/components/TracerouteResult.vue'
import { useNodes } from '@/composables/useNodes'
import { useProbeTask } from '@/composables/useProbeTask'
import { exportSnapshotCSV, exportSnapshotJSON } from '@/utils/export'

const { nodes, onlineNodes } = useNodes()
const { snapshot, resultsList, running, errorMsg, startSingle, stopAll } = useProbeTask()

const target = ref('')
const maxHops = ref(30)
const queriesPerHop = ref(3)
const selectedNodeIDs = ref<string[]>([])
const ipFilter = ref('')
const initializedSelection = ref(false)

watch(
  onlineNodes,
  (list) => {
    if (!initializedSelection.value && list.length > 0) {
      // Default select up to first 5 online nodes for traceroute to avoid overwhelming nodes, user can select all.
      selectedNodeIDs.value = list.slice(0, 5).map((n) => n.id)
      initializedSelection.value = true
    }
  },
  { immediate: true }
)

function handleStart() {
  if (!target.value.trim()) return
  ipFilter.value = ''
  void startSingle({
    type: 'traceroute',
    target: target.value.trim(),
    node_ids: selectedNodeIDs.value.length > 0 ? selectedNodeIDs.value : undefined,
    timeout_ms: 40000,
    params: {
      traceroute: {
        max_hops: maxHops.value,
        queries_per_hop: queriesPerHop.value,
        protocol: 'icmp'
      }
    }
  })
}
</script>

<template>
  <div>
    <div class="card">
      <div class="card-body">
        <form class="probe-form-row" @submit.prevent="handleStart">
          <div class="target-input-wrap">
            <span class="target-prefix">Traceroute</span>
            <input
              v-model="target"
              type="text"
              class="target-input"
              placeholder="请输入目标域名或 DN11 IP，如 172.16.20.1"
            />
          </div>

          <div class="inline-field">
            <label>最大跳数</label>
            <input v-model.number="maxHops" type="number" min="1" max="64" style="width: 52px" />
          </div>

          <button v-if="!running" type="submit" class="btn btn-primary" :disabled="!target.trim()">
            开始追踪
          </button>
          <button v-else type="button" class="btn btn-danger" @click="stopAll">
            取消追踪
          </button>
        </form>

        <NodeFilterBar v-model:selected-ids="selectedNodeIDs" :nodes="nodes" />
      </div>
    </div>

    <div v-if="errorMsg" class="error-banner">{{ errorMsg }}</div>

    <template v-if="snapshot">
      <SummaryStatsBar
        :snapshot="snapshot"
        v-model:selected-i-p-filter="ipFilter"
        @export-csv="exportSnapshotCSV(snapshot)"
        @export-json="exportSnapshotJSON(snapshot)"
      />
      <TracerouteResult :results="resultsList" />
    </template>
  </div>
</template>
