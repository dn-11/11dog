import { computed, onMounted, onUnmounted, ref } from 'vue'
import { fetchMeta, fetchNodes } from '@/api/client'
import type { MetaInfo, PublicNodeInfo } from '@/types/api'

const nodes = ref<PublicNodeInfo[]>([])
const meta = ref<MetaInfo | null>(null)
const loading = ref(false)
const loadError = ref('')
let refCount = 0
let timer: number | undefined

async function refresh() {
  loading.value = true
  try {
    const [nodeList, metaData] = await Promise.all([fetchNodes(), fetchMeta()])
    nodes.value = nodeList
    meta.value = metaData
    loadError.value = ''
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : '加载节点失败'
  } finally {
    loading.value = false
  }
}

export function useNodes() {
  onMounted(() => {
    refCount++
    if (refCount === 1) {
      void refresh()
      timer = window.setInterval(() => {
        void refresh()
      }, 10000)
    }
  })

  onUnmounted(() => {
    refCount--
    if (refCount <= 0 && timer !== undefined) {
      window.clearInterval(timer)
      timer = undefined
      refCount = 0
    }
  })

  const onlineNodes = computed(() => nodes.value.filter((n) => n.status === 'online'))

  const regions = computed(() => {
    const set = new Set<string>()
    for (const n of onlineNodes.value) {
      if (n.region) set.add(n.region)
    }
    return Array.from(set).sort()
  })

  const groups = computed(() => {
    const set = new Set<string>()
    for (const n of onlineNodes.value) {
      if (n.group) set.add(n.group)
    }
    return Array.from(set).sort()
  })

  return {
    nodes,
    onlineNodes,
    meta,
    regions,
    groups,
    loading,
    loadError,
    refresh
  }
}
