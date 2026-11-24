<script setup lang="ts">
import { ref, watch } from 'vue'
import NodeFilterBar from '@/components/NodeFilterBar.vue'
import ResultTable from '@/components/ResultTable.vue'
import SummaryStatsBar from '@/components/SummaryStatsBar.vue'
import { useNodes } from '@/composables/useNodes'
import { useProbeTask } from '@/composables/useProbeTask'
import { exportSnapshotCSV, exportSnapshotJSON } from '@/utils/export'

const { nodes, onlineNodes } = useNodes()
const { snapshot, resultsList, running, errorMsg, startSingle } = useProbeTask()

const target = ref('')
const recordType = ref('A')
const dnsServer = ref('')
const selectedNodeIDs = ref<string[]>([])
const ipFilter = ref('')
const initializedSelection = ref(false)

watch(
  onlineNodes,
  (list) => {
    if (!initializedSelection.value && list.length > 0) {
      selectedNodeIDs.value = list.map((n) => n.id)
      initializedSelection.value = true
    }
  },
  { immediate: true }
)

function handleStart() {
  if (!target.value.trim()) return
  ipFilter.value = ''
  void startSingle({
    type: 'dns',
    target: target.value.trim(),
    node_ids: selectedNodeIDs.value.length > 0 ? selectedNodeIDs.value : undefined,
    params: {
      dns: {
        record_type: recordType.value,
        dns_server: dnsServer.value.trim() || undefined
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
            <span class="target-prefix">DNS</span>
            <input
              v-model="target"
              type="text"
              class="target-input"
              placeholder="请输入待解析域名，如 example.dn11"
            />
          </div>

          <div class="inline-field">
            <label>记录</label>
            <select v-model="recordType">
              <option value="A">A</option>
              <option value="AAAA">AAAA</option>
              <option value="CNAME">CNAME</option>
              <option value="MX">MX</option>
              <option value="TXT">TXT</option>
              <option value="NS">NS</option>
              <option value="PTR">PTR</option>
              <option value="SOA">SOA</option>
            </select>
          </div>

          <div class="inline-field">
            <label>DNS</label>
            <input v-model="dnsServer" type="text" placeholder="默认系统DNS" style="width: 120px" />
          </div>

          <button type="submit" class="btn btn-primary" :disabled="!target.trim() || running">
            {{ running ? '查询中...' : '立即查询' }}
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
      <ResultTable probe-type="dns" :results="resultsList" :ip-filter="ipFilter" @clear-ip-filter="ipFilter = ''" />
    </template>
  </div>
</template>
