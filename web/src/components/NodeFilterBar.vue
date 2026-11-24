<script setup lang="ts">
import { computed, ref } from 'vue'
import type { PublicNodeInfo } from '@/types/api'

const props = defineProps<{
  nodes: PublicNodeInfo[]
  selectedIds: string[]
}>()

const emit = defineEmits<{
  (e: 'update:selectedIds', ids: string[]): void
}>()

const expanded = ref(false)
const activeRegion = ref<string>('ALL')
const activeGroup = ref<string>('ALL')

const onlineNodes = computed(() => props.nodes.filter((n) => n.status === 'online'))

const regions = computed(() => {
  const s = new Set<string>()
  for (const n of onlineNodes.value) {
    if (n.region) s.add(n.region)
  }
  return Array.from(s).sort()
})

const groups = computed(() => {
  const s = new Set<string>()
  for (const n of onlineNodes.value) {
    if (n.group) s.add(n.group)
  }
  return Array.from(s).sort()
})

const filteredNodes = computed(() => {
  return onlineNodes.value.filter((n) => {
    if (activeRegion.value !== 'ALL' && n.region !== activeRegion.value) return false
    if (activeGroup.value !== 'ALL' && n.group !== activeGroup.value) return false
    return true
  })
})

const selectedSet = computed(() => new Set(props.selectedIds))

function applyFilter(region: string, group: string) {
  activeRegion.value = region
  activeGroup.value = group
  const matched = onlineNodes.value
    .filter((n) => {
      if (region !== 'ALL' && n.region !== region) return false
      if (group !== 'ALL' && n.group !== group) return false
      return true
    })
    .map((n) => n.id)
  emit('update:selectedIds', matched)
}

function selectAllOnline() {
  activeRegion.value = 'ALL'
  activeGroup.value = 'ALL'
  emit(
    'update:selectedIds',
    onlineNodes.value.map((n) => n.id)
  )
}

function clearSelection() {
  emit('update:selectedIds', [])
}

function toggleNode(id: string) {
  const next = new Set(props.selectedIds)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  emit('update:selectedIds', Array.from(next))
}
</script>

<template>
  <div class="node-filter-bar">
    <div class="filter-row">
      <span class="filter-label">区域筛选：</span>
      <button
        type="button"
        class="chip"
        :class="{ active: activeRegion === 'ALL' }"
        @click="applyFilter('ALL', activeGroup)"
      >
        全部区域 ({{ onlineNodes.length }})
      </button>
      <button
        v-for="r in regions"
        :key="r"
        type="button"
        class="chip"
        :class="{ active: activeRegion === r }"
        @click="applyFilter(r, activeGroup)"
      >
        {{ r }}
      </button>

      <span v-if="groups.length > 0" class="divider"></span>
      <span v-if="groups.length > 0" class="filter-label">网络组：</span>
      <button
        v-for="g in groups"
        :key="g"
        type="button"
        class="chip"
        :class="{ active: activeGroup === g }"
        @click="applyFilter(activeRegion, activeGroup === g ? 'ALL' : g)"
      >
        {{ g }}
      </button>

      <div class="filter-actions">
        <button type="button" class="link-btn" @click="selectAllOnline">全选</button>
        <button type="button" class="link-btn" @click="clearSelection">清空</button>
        <button type="button" class="link-btn primary" @click="expanded = !expanded">
          {{ expanded ? '收起节点' : `节点明细 (${selectedIds.length}/${onlineNodes.length})` }}
        </button>
      </div>
    </div>

    <div v-if="expanded" class="node-grid">
      <label
        v-for="n in filteredNodes"
        :key="n.id"
        class="node-check-item"
        :class="{ checked: selectedSet.has(n.id) }"
      >
        <input
          type="checkbox"
          :checked="selectedSet.has(n.id)"
          @change="toggleNode(n.id)"
        />
        <span class="node-name">{{ n.name }}</span>
        <span class="node-meta mono">
          {{ n.region }} · {{ n.group }}
          <template v-if="n.asn">· AS{{ n.asn }}</template>
        </span>
      </label>
      <div v-if="filteredNodes.length === 0" class="empty-nodes">
        当前筛选条件下暂无在线探测节点
      </div>
    </div>
  </div>
</template>

<style scoped>
.node-filter-bar {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border-color);
}

.filter-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.filter-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
}

.chip {
  border: 1px solid var(--border-strong);
  background: #f8fafc;
  color: var(--text-secondary);
  border-radius: 999px;
  padding: 3px 11px;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.12s ease;
}

.chip:hover {
  border-color: var(--brand-blue);
  color: var(--brand-blue);
}

.chip.active {
  background: var(--brand-blue);
  border-color: var(--brand-blue);
  color: #fff;
  font-weight: 600;
}

.divider {
  width: 1px;
  height: 16px;
  background: var(--border-strong);
  margin: 0 4px;
}

.filter-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}

.link-btn {
  border: none;
  background: none;
  padding: 2px 4px;
  font-size: 12px;
  color: var(--text-secondary);
  cursor: pointer;
}

.link-btn:hover {
  color: var(--brand-blue);
  text-decoration: underline;
}

.link-btn.primary {
  color: var(--brand-blue);
  font-weight: 600;
}

.node-grid {
  margin-top: 10px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 8px;
  max-height: 220px;
  overflow-y: auto;
  padding: 10px;
  background: #f8fafc;
  border: 1px solid var(--border-color);
  border-radius: 6px;
}

.node-check-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  background: #fff;
  border: 1px solid var(--border-color);
  border-radius: 5px;
  cursor: pointer;
  font-size: 12px;
}

.node-check-item.checked {
  border-color: #93c5fd;
  background: #eff6ff;
}

.node-name {
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.node-meta {
  color: var(--text-muted);
  font-size: 11px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.node-load {
  margin-left: auto;
  font-size: 11px;
  color: var(--text-secondary);
  background: #f1f5f9;
  padding: 1px 5px;
  border-radius: 4px;
}

.empty-nodes {
  grid-column: 1 / -1;
  text-align: center;
  color: var(--text-muted);
  padding: 12px;
  font-size: 12px;
}
</style>
