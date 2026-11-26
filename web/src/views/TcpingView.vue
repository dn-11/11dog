<script setup lang="ts">
import { ref, watch } from 'vue'
import ContinuousChart from '@/components/ContinuousChart.vue'
import ContinuousTable from '@/components/ContinuousTable.vue'
import LatencyVisual from '@/components/LatencyVisual.vue'
import NodeFilterBar from '@/components/NodeFilterBar.vue'
import ResultTable from '@/components/ResultTable.vue'
import SummaryStatsBar from '@/components/SummaryStatsBar.vue'
import { useNodes } from '@/composables/useNodes'
import { useProbeTask } from '@/composables/useProbeTask'
import { exportContinuousCSV, exportContinuousJSON, exportSnapshotCSV, exportSnapshotJSON } from '@/utils/export'

const { nodes, onlineNodes } = useNodes()
const {
  mode,
  snapshot,
  continuousSession,
  resultsList,
  running,
  continuous,
  errorMsg,
  startSingle,
  startContinuous,
  pauseContinuous,
  resumeContinuous,
  clearContinuous,
  stopAll
} = useProbeTask()

const target = ref('')
const port = ref(80)
const count = ref(4)
const intervalMs = ref(300)
const continuousSec = ref(2)
const continuousMaxRounds = ref(0)
const showAdvanced = ref(false)
const selectedNodeIDs = ref<string[]>([])
const checkedChartIDs = ref<string[]>([])
const chartSelectionSeeded = ref(false)
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

watch(
  () => (continuousSession.value ? Object.keys(continuousSession.value.nodes) : []),
  (keys) => {
    if (!chartSelectionSeeded.value && keys.length > 0) {
      checkedChartIDs.value = keys.slice(0, 6)
      chartSelectionSeeded.value = true
    }
  }
)

function buildReq() {
  return {
    type: 'tcping' as const,
    target: target.value.trim(),
    node_ids: selectedNodeIDs.value.length > 0 ? selectedNodeIDs.value : undefined,
    params: {
      tcping: {
        port: port.value,
        count: count.value,
        interval_ms: intervalMs.value
      }
    }
  }
}

function handleSingle() {
  if (!target.value.trim()) return
  ipFilter.value = ''
  void startSingle(buildReq())
}

function handleContinuous() {
  if (!target.value.trim()) return
  ipFilter.value = ''
  chartSelectionSeeded.value = false
  checkedChartIDs.value = []
  void startContinuous(buildReq(), {
    intervalSec: continuousSec.value,
    maxRounds: continuousMaxRounds.value
  })
}
</script>

<template>
  <div>
    <div class="card">
      <div class="card-body">
        <form class="probe-form-row" @submit.prevent="handleSingle">
          <div class="target-input-wrap">
            <span class="target-prefix">TCPing</span>
            <input
              v-model="target"
              type="text"
              class="target-input"
              placeholder="请输入 DN11 域名或 IP，如 172.16.0.1"
            />
          </div>

          <div class="inline-field">
            <label>端口</label>
            <input v-model.number="port" type="number" min="1" max="65535" style="width: 68px" />
          </div>

          <button type="submit" class="btn btn-primary" :disabled="!target.trim() || (running && !continuous)">
            {{ running && !continuous ? '探测中...' : '单次测试' }}
          </button>

          <button
            v-if="!continuous"
            type="button"
            class="btn btn-success"
            :disabled="!target.trim()"
            @click="handleContinuous"
          >
            持续测试
          </button>
          <button v-else type="button" class="btn btn-danger" @click="stopAll">
            停止测试
          </button>

          <div class="inline-field" title="持续测试发包频率">
            <label>频率</label>
            <select v-model.number="continuousSec" :disabled="continuous">
              <option :value="1">1秒/次</option>
              <option :value="2">2秒/次</option>
              <option :value="3">3秒/次</option>
              <option :value="5">5秒/次</option>
            </select>
          </div>

          <button type="button" class="btn btn-outline" @click="showAdvanced = !showAdvanced">
            {{ showAdvanced ? '收起参数' : '高级参数' }}
          </button>
        </form>

        <div v-if="showAdvanced" class="advanced-row">
          <label class="form-control">
            单次探测次数:
            <input v-model.number="count" type="number" min="1" max="20" style="width: 68px" />
          </label>
          <label class="form-control">
            包间隔(ms):
            <input v-model.number="intervalMs" type="number" min="100" max="5000" style="width: 78px" />
          </label>
          <label class="form-control">
            持续总轮数:
            <select v-model.number="continuousMaxRounds" :disabled="continuous">
              <option :value="0">持续不停</option>
              <option :value="50">50 次</option>
              <option :value="100">100 次</option>
              <option :value="300">300 次</option>
            </select>
          </label>
        </div>

        <NodeFilterBar v-model:selected-ids="selectedNodeIDs" :nodes="nodes" />
      </div>
    </div>

    <div v-if="errorMsg" class="error-banner">{{ errorMsg }}</div>

    <!-- Continuous Test Mode (ITDog style) -->
    <template v-if="mode === 'continuous' && continuousSession">
      <ContinuousChart
        :session="continuousSession"
        v-model:checked-chart-i-ds="checkedChartIDs"
        @pause="pauseContinuous"
        @resume="resumeContinuous"
        @clear="clearContinuous"
        @export-csv="exportContinuousCSV(continuousSession)"
        @export-json="exportContinuousJSON(continuousSession)"
      />
      <ContinuousTable :session="continuousSession" v-model:checked-chart-i-ds="checkedChartIDs" />
    </template>

    <!-- Single-Shot Mode -->
    <template v-else-if="mode === 'single' && snapshot">
      <SummaryStatsBar
        :snapshot="snapshot"
        v-model:selected-i-p-filter="ipFilter"
        @export-csv="exportSnapshotCSV(snapshot)"
        @export-json="exportSnapshotJSON(snapshot)"
      />
      <LatencyVisual :results="resultsList" />
      <ResultTable probe-type="tcping" :results="resultsList" :ip-filter="ipFilter" @clear-ip-filter="ipFilter = ''" />
    </template>
  </div>
</template>
