import type { NodeTaskResult, TaskStatus } from '@/types/api'

export function latencyColor(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '#64748b'
  if (ms < 30) return '#15803d'
  if (ms < 80) return '#22c55e'
  if (ms < 150) return '#eab308'
  if (ms < 250) return '#f97316'
  return '#ef4444'
}

export function latencyBucketLabel(ms: number): string {
  if (ms < 30) return '< 30ms (极快)'
  if (ms < 80) return '30~80ms (良好)'
  if (ms < 150) return '80~150ms (一般)'
  if (ms < 250) return '150~250ms (较慢)'
  return '> 250ms (很慢)'
}

export function extractPrimaryLatencyMs(res: NodeTaskResult): number | null {
  if (res.status !== 'completed') return null
  if (res.ping) {
    if (res.ping.received === 0) return null
    return res.ping.avg_rtt_ms
  }
  if (res.tcping) {
    if (res.tcping.connected === 0) return null
    return res.tcping.avg_rtt_ms
  }
  if (res.http?.timing) {
    return res.http.timing.total_ms
  }
  if (res.dns) {
    return res.dns.query_time_ms
  }
  if (res.traceroute && res.traceroute.hops.length > 0) {
    const last = [...res.traceroute.hops].reverse().find((h) => !h.timeout && h.avg_rtt_ms > 0)
    return last ? last.avg_rtt_ms : null
  }
  return null
}

export function formatMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '-'
  return `${ms.toFixed(ms < 10 ? 2 : 1)} ms`
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined) return '-'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

export function statusLabel(status: TaskStatus): string {
  switch (status) {
    case 'queued':
      return '等待中'
    case 'running':
      return '探测中'
    case 'completed':
      return '成功'
    case 'timeout':
      return '超时'
    case 'error':
      return '失败'
    case 'overloaded':
      return '节点繁忙'
    case 'expired':
      return '等待超时'
    case 'cancelled':
      return '已取消'
    default:
      return status
  }
}

export function statusBadgeClass(status: TaskStatus): string {
  switch (status) {
    case 'completed':
      return 'badge-green'
    case 'running':
    case 'queued':
      return 'badge-blue'
    case 'overloaded':
    case 'expired':
      return 'badge-purple'
    case 'timeout':
      return 'badge-amber'
    case 'error':
      return 'badge-red'
    default:
      return 'badge-slate'
  }
}
