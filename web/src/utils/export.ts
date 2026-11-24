import type { ContinuousSession } from '@/composables/useProbeTask'
import type { TaskSnapshot } from '@/types/api'
import { extractPrimaryLatencyMs, statusLabel } from './format'

function triggerDownload(filename: string, content: string, mime: string) {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

function csvCell(v: unknown): string {
  const s = String(v ?? '')
  if (s.includes(',') || s.includes('"') || s.includes('\n')) {
    return `"${s.replace(/"/g, '""')}"`
  }
  return s
}

export function exportSnapshotJSON(snap: TaskSnapshot) {
  triggerDownload(
    `11dog-${snap.type}-${snap.task_id}.json`,
    JSON.stringify(snap, null, 2),
    'application/json;charset=utf-8'
  )
}

export function exportSnapshotCSV(snap: TaskSnapshot) {
  const headers = [
    '区域',
    '节点名称',
    '节点ID',
    '网络组',
    '节点ASN',
    '状态',
    '响应IP',
    'DN11归属ASN',
    'DN11归属名称',
    '主耗时(ms)',
    '丢包/状态细节',
    '错误信息'
  ]
  const rows = [headers.map(csvCell).join(',')]
  for (const r of Object.values(snap.results)) {
    const ms = extractPrimaryLatencyMs(r)
    let detail = ''
    if (r.ping) detail = `loss=${r.ping.loss_percent}% ttl=${r.ping.ttl}`
    else if (r.tcping) detail = `port=${r.tcping.port} state=${r.tcping.state} loss=${r.tcping.loss_percent}%`
    else if (r.http) detail = `HTTP ${r.http.status_code} size=${r.http.content_length}`
    else if (r.dns) detail = `${r.dns.rcode} (${r.dns.records.length} records)`
    else if (r.traceroute) detail = `hops=${r.traceroute.hops.length} reached=${r.traceroute.reached}`

    rows.push(
      [
        r.region,
        r.node_name,
        r.node_id,
        r.group,
        r.node_asn || '',
        statusLabel(r.status),
        r.resolved_ip ?? '',
        r.resolved_dn11?.asn ?? '',
        r.resolved_dn11?.owner ?? '',
        ms !== null ? ms.toFixed(2) : '',
        detail,
        r.error_message ?? ''
      ]
        .map(csvCell)
        .join(',')
    )
  }
  triggerDownload(`11dog-${snap.type}-${snap.task_id}.csv`, '\uFEFF' + rows.join('\n'), 'text/csv;charset=utf-8')
}

export function exportContinuousJSON(session: ContinuousSession) {
  const clean = {
    probe_type: session.probeType,
    target: session.target,
    round: session.round,
    interval_sec: session.intervalSec,
    elapsed_sec: session.elapsedSec,
    nodes: Object.values(session.nodes).map((n) => ({
      node_id: n.node_id,
      node_name: n.node_name,
      region: n.region,
      group: n.group,
      node_asn: n.node_asn,
      resolved_ip: n.resolved_ip,
      resolved_dn11: n.resolved_dn11,
      sent: n.sent,
      received: n.received,
      lost: n.lost,
      loss_percent: n.loss_percent,
      last_ms: n.last_ms,
      min_ms: n.min_ms,
      avg_ms: n.avg_ms,
      max_ms: n.max_ms,
      jitter_ms: n.jitter_ms,
      samples: n.samples
    }))
  }
  triggerDownload(
    `11dog-continuous-${session.probeType}-${Date.now()}.json`,
    JSON.stringify(clean, null, 2),
    'application/json;charset=utf-8'
  )
}

export function exportContinuousCSV(session: ContinuousSession) {
  const headers = [
    '区域',
    '节点名称',
    '节点ID',
    '网络组',
    '响应IP',
    'DN11归属',
    '发包数',
    '成功数',
    '丢包数',
    '丢包率(%)',
    '最新延迟(ms)',
    '最快(ms)',
    '平均(ms)',
    '最慢(ms)',
    '抖动(ms)'
  ]
  const rows = [headers.map(csvCell).join(',')]
  for (const n of Object.values(session.nodes)) {
    rows.push(
      [
        n.region,
        n.node_name,
        n.node_id,
        n.group,
        n.resolved_ip ?? '',
        n.resolved_dn11 ? `AS${n.resolved_dn11.asn} ${n.resolved_dn11.owner}` : '',
        n.sent,
        n.received,
        n.lost,
        n.loss_percent.toFixed(2),
        n.last_ms !== null ? n.last_ms.toFixed(2) : '',
        n.min_ms !== null ? n.min_ms.toFixed(2) : '',
        n.avg_ms !== null ? n.avg_ms.toFixed(2) : '',
        n.max_ms !== null ? n.max_ms.toFixed(2) : '',
        n.jitter_ms !== null ? n.jitter_ms.toFixed(2) : ''
      ]
        .map(csvCell)
        .join(',')
    )
  }
  triggerDownload(
    `11dog-continuous-${session.probeType}-${Date.now()}.csv`,
    '\uFEFF' + rows.join('\n'),
    'text/csv;charset=utf-8'
  )
}
