import { computed, onUnmounted, ref } from 'vue'
import { cancelTask, createTask, subscribeTaskStream } from '@/api/client'
import type { CreateTaskRequest, DN11Match, NodeTaskResult, ProbeType, TaskSnapshot, TaskStatus } from '@/types/api'
import { extractPrimaryLatencyMs } from '@/utils/format'

export interface HistorySample {
  timestamp: number
  avgMs: number | null
  minMs: number | null
  maxMs: number | null
  lossPct: number
}

export interface ContinuousTickPoint {
  round: number
  ts: number
  ms: number | null
  status: TaskStatus
  httpCode?: number
  ttl?: number
}

export interface ContinuousNodeStats {
  node_id: string
  node_name: string
  region: string
  group: string
  node_asn: number
  resolved_ip?: string
  resolved_dn11?: DN11Match
  sent: number
  received: number
  lost: number
  busy: number
  loss_percent: number
  last_ms: number | null
  last_status: TaskStatus
  last_ttl?: number
  last_http_code?: number
  last_error?: string
  min_ms: number | null
  max_ms: number | null
  avg_ms: number | null
  jitter_ms: number | null
  samples: ContinuousTickPoint[]
  // Internal accumulators
  _sumMs: number
  _lastValidMs: number | null
  _sumJitter: number
  _jitterCount: number
}

export interface ContinuousSession {
  probeType: ProbeType
  target: string
  active: boolean
  paused: boolean
  round: number
  maxRounds: number // 0 = unlimited
  intervalSec: number
  startedAt: number
  elapsedSec: number
  timestamps: { round: number; ts: number }[]
  nodes: Record<string, ContinuousNodeStats>
}

export interface ContinuousOptions {
  intervalSec?: number
  maxRounds?: number
}

const MAX_HISTORY_TICKS = 120

function round2(n: number): number {
  return Math.round(n * 100) / 100
}

export function useProbeTask() {
  const mode = ref<'single' | 'continuous'>('single')
  const snapshot = ref<TaskSnapshot | null>(null)
  const continuousSession = ref<ContinuousSession | null>(null)
  const running = ref(false)
  const continuous = ref(false)
  const errorMsg = ref('')
  const continuousSamples = ref<HistorySample[]>([])

  let closeStream: (() => void) | null = null
  let nextRunTimer: number | undefined
  let elapsedTimer: number | undefined
  let lastReq: CreateTaskRequest | null = null
  let activeTaskID = ''

  function cleanupStream() {
    if (closeStream) {
      closeStream()
      closeStream = null
    }
    activeTaskID = ''
  }

  function clearTimers() {
    if (nextRunTimer !== undefined) {
      window.clearTimeout(nextRunTimer)
      nextRunTimer = undefined
    }
    if (elapsedTimer !== undefined) {
      window.clearInterval(elapsedTimer)
      elapsedTimer = undefined
    }
  }

  function stopContinuousLoop() {
    continuous.value = false
    clearTimers()
    if (continuousSession.value) {
      continuousSession.value.active = false
      continuousSession.value.paused = false
    }
  }

  async function stopAll() {
    stopContinuousLoop()
    const pendingTaskID = activeTaskID
    cleanupStream()
    running.value = false
    if (pendingTaskID) {
      try {
        const cancelled = await cancelTask(pendingTaskID)
        if (mode.value === 'single') {
          snapshot.value = cancelled
        }
      } catch {
        // ignore cancel error
      }
    }
  }

  async function startSingle(req: CreateTaskRequest) {
    stopContinuousLoop()
    mode.value = 'single'
    lastReq = req
    await executeSingleOnce(req)
  }

  async function executeSingleOnce(req: CreateTaskRequest) {
    cleanupStream()
    errorMsg.value = ''
    running.value = true

    try {
      const created = await createTask(req)
      snapshot.value = created
      activeTaskID = created.task_id

      closeStream = subscribeTaskStream(created.task_id, {
        onSnapshot(snap) {
          snapshot.value = snap
        },
        onProgress(ev) {
          if (!snapshot.value || !ev.node_id) return
          const cur = snapshot.value.results[ev.node_id]
          if (!cur) return
          if (
            ev.status &&
            !['completed', 'timeout', 'error', 'overloaded', 'expired', 'cancelled'].includes(cur.status)
          ) {
            cur.status = ev.status
          }
          if (ev.progress?.traceroute_hop) {
            if (!cur.traceroute) {
              cur.traceroute = { reached: false, hops: [] }
            }
            const hop = ev.progress.traceroute_hop
            const idx = cur.traceroute.hops.findIndex((h) => h.hop === hop.hop)
            if (idx >= 0) {
              cur.traceroute.hops[idx] = hop
            } else {
              cur.traceroute.hops.push(hop)
              cur.traceroute.hops.sort((a, b) => a.hop - b.hop)
            }
          }
          if (ev.progress?.ping_packet) {
            if (!cur.ping) {
              cur.ping = {
                sent: 0,
                received: 0,
                loss_percent: 0,
                min_rtt_ms: 0,
                avg_rtt_ms: 0,
                max_rtt_ms: 0,
                mdev_rtt_ms: 0,
                ttl: 0,
                packets: []
              }
            }
            cur.ping.packets.push(ev.progress.ping_packet)
          }
        },
        onResult(ev) {
          if (!snapshot.value || !ev.node_id) return
          if (ev.result) {
            snapshot.value.results[ev.node_id] = ev.result
          }
          recalcSnapshotCounts(snapshot.value)
        },
        onDone(doneSnap) {
          snapshot.value = doneSnap
          running.value = false
          cleanupStream()
        },
        onError(msg) {
          errorMsg.value = msg
          running.value = false
        }
      })
    } catch (err) {
      errorMsg.value = err instanceof Error ? err.message : '创建任务失败'
      running.value = false
    }
  }

  function buildContinuousTickReq(base: CreateTaskRequest): CreateTaskRequest {
    const cloned: CreateTaskRequest = {
      type: base.type,
      target: base.target,
      node_ids: base.node_ids ? [...base.node_ids] : undefined,
      regions: base.regions ? [...base.regions] : undefined,
      groups: base.groups ? [...base.groups] : undefined,
      params: {}
    }

    if (base.type === 'ping') {
      cloned.timeout_ms = Math.min(base.timeout_ms ?? 3000, 4000)
      cloned.params = {
        ping: {
          ...base.params?.ping,
          count: 1
        }
      }
    } else if (base.type === 'tcping') {
      cloned.timeout_ms = Math.min(base.timeout_ms ?? 3000, 4000)
      cloned.params = {
        tcping: {
          port: base.params?.tcping?.port ?? 80,
          ...base.params?.tcping,
          count: 1
        }
      }
    } else if (base.type === 'http') {
      cloned.timeout_ms = Math.min(base.timeout_ms ?? 8000, 10000)
      cloned.params = {
        http: { ...base.params?.http }
      }
    }
    return cloned
  }

  function ensureContinuousNode(session: ContinuousSession, r: NodeTaskResult): ContinuousNodeStats {
    let entry = session.nodes[r.node_id]
    if (!entry) {
      entry = {
        node_id: r.node_id,
        node_name: r.node_name,
        region: r.region,
        group: r.group,
        node_asn: r.node_asn,
        sent: 0,
        received: 0,
        lost: 0,
        busy: 0,
        loss_percent: 0,
        last_ms: null,
        last_status: r.status,
        min_ms: null,
        max_ms: null,
        avg_ms: null,
        jitter_ms: null,
        samples: [],
        _sumMs: 0,
        _lastValidMs: null,
        _sumJitter: 0,
        _jitterCount: 0
      }
      session.nodes[r.node_id] = entry
    }
    if (r.node_name) entry.node_name = r.node_name
    if (r.region) entry.region = r.region
    if (r.group) entry.group = r.group
    if (r.node_asn) entry.node_asn = r.node_asn
    return entry
  }

  function recordNodeTick(session: ContinuousSession, r: NodeTaskResult, round: number, ts: number) {
    const entry = ensureContinuousNode(session, r)
    if (r.resolved_ip) entry.resolved_ip = r.resolved_ip
    if (r.resolved_dn11) entry.resolved_dn11 = r.resolved_dn11

    const ms = extractPrimaryLatencyMs(r)
    entry.sent++
    entry.last_status = r.status
    entry.last_error = r.error_message
    if (r.ping?.ttl) entry.last_ttl = r.ping.ttl
    if (r.http?.status_code) entry.last_http_code = r.http.status_code

    if (ms !== null) {
      entry.received++
      entry._sumMs += ms
      entry.avg_ms = round2(entry._sumMs / entry.received)
      entry.min_ms = entry.min_ms === null ? ms : Math.min(entry.min_ms, ms)
      entry.max_ms = entry.max_ms === null ? ms : Math.max(entry.max_ms, ms)
      if (entry._lastValidMs !== null) {
        entry._sumJitter += Math.abs(ms - entry._lastValidMs)
        entry._jitterCount++
        entry.jitter_ms = round2(entry._sumJitter / entry._jitterCount)
      }
      entry._lastValidMs = ms
      entry.last_ms = ms
    } else {
      entry.lost++
      if (r.status === 'overloaded' || r.status === 'expired') {
        entry.busy++
      }
      entry.last_ms = null
    }

    entry.loss_percent = entry.sent > 0 ? round2((entry.lost / entry.sent) * 100) : 0
    entry.samples.push({
      round,
      ts,
      ms,
      status: r.status,
      httpCode: r.http?.status_code,
      ttl: r.ping?.ttl
    })
    if (entry.samples.length > MAX_HISTORY_TICKS) {
      entry.samples.shift()
    }
  }

  async function startContinuous(req: CreateTaskRequest, options: ContinuousOptions = {}) {
    stopContinuousLoop()
    cleanupStream()

    const intervalSec = Math.max(1, Math.min(60, options.intervalSec ?? 2))
    const maxRounds = Math.max(0, options.maxRounds ?? 0)

    mode.value = 'continuous'
    errorMsg.value = ''
    running.value = true
    continuous.value = true
    lastReq = req

    continuousSession.value = {
      probeType: req.type,
      target: req.target,
      active: true,
      paused: false,
      round: 0,
      maxRounds,
      intervalSec,
      startedAt: Date.now(),
      elapsedSec: 0,
      timestamps: [],
      nodes: {}
    }

    elapsedTimer = window.setInterval(() => {
      if (continuousSession.value?.active && !continuousSession.value.paused) {
        continuousSession.value.elapsedSec++
      }
    }, 1000)

    await runContinuousTick()
  }

  async function runContinuousTick() {
    const session = continuousSession.value
    if (!session || !session.active || session.paused || !lastReq) return

    cleanupStream()
    const tickReq = buildContinuousTickReq(lastReq)
    const roundNum = session.round + 1
    const tickStartMs = Date.now()
    session.round = roundNum
    session.timestamps.push({ round: roundNum, ts: tickStartMs })
    if (session.timestamps.length > MAX_HISTORY_TICKS) {
      session.timestamps.shift()
    }

    const recordedNodes = new Set<string>()
    let finished = false

    const scheduleNext = () => {
      if (!session.active || session.paused) return
      if (session.maxRounds > 0 && session.round >= session.maxRounds) {
        stopContinuousLoop()
        running.value = false
        return
      }
      const elapsed = Date.now() - tickStartMs
      const delay = Math.max(200, session.intervalSec * 1000 - elapsed)
      nextRunTimer = window.setTimeout(() => {
        void runContinuousTick()
      }, delay)
    }

    try {
      const created = await createTask(tickReq)
      if (!session.active) return
      activeTaskID = created.task_id
      errorMsg.value = ''

      for (const r of Object.values(created.results)) {
        ensureContinuousNode(session, r)
      }

      closeStream = subscribeTaskStream(created.task_id, {
        onSnapshot(snap) {
          if (!session.active) return
          for (const r of Object.values(snap.results)) {
            ensureContinuousNode(session, r)
            if (
              ['completed', 'timeout', 'error', 'overloaded', 'expired', 'cancelled'].includes(r.status) &&
              !recordedNodes.has(r.node_id)
            ) {
              recordedNodes.add(r.node_id)
              recordNodeTick(session, r, roundNum, tickStartMs)
            }
          }
        },
        onProgress() {
          // Single packet per tick doesn't need incremental packet updates
        },
        onResult(ev) {
          if (!session.active || !ev.result) return
          if (!recordedNodes.has(ev.result.node_id)) {
            recordedNodes.add(ev.result.node_id)
            recordNodeTick(session, ev.result, roundNum, tickStartMs)
          }
        },
        onDone(doneSnap) {
          if (finished) return
          finished = true
          for (const r of Object.values(doneSnap.results)) {
            if (!recordedNodes.has(r.node_id)) {
              recordedNodes.add(r.node_id)
              recordNodeTick(session, r, roundNum, tickStartMs)
            }
          }
          cleanupStream()
          scheduleNext()
        },
        onError(msg) {
          if (finished) return
          finished = true
          errorMsg.value = msg
          cleanupStream()
          scheduleNext()
        }
      })
    } catch (err) {
      errorMsg.value = err instanceof Error ? err.message : '持续探测请求失败'
      cleanupStream()
      if (session.active && !session.paused) {
        nextRunTimer = window.setTimeout(() => {
          void runContinuousTick()
        }, Math.max(2000, session.intervalSec * 1000))
      }
    }
  }

  function pauseContinuous() {
    if (!continuousSession.value || !continuousSession.value.active) return
    continuousSession.value.paused = true
    if (nextRunTimer !== undefined) {
      window.clearTimeout(nextRunTimer)
      nextRunTimer = undefined
    }
    cleanupStream()
  }

  function resumeContinuous() {
    if (!continuousSession.value || !continuousSession.value.active || !continuousSession.value.paused) return
    continuousSession.value.paused = false
    void runContinuousTick()
  }

  function clearContinuous() {
    if (!continuousSession.value) return
    continuousSession.value.round = 0
    continuousSession.value.elapsedSec = 0
    continuousSession.value.startedAt = Date.now()
    continuousSession.value.timestamps = []
    for (const n of Object.values(continuousSession.value.nodes)) {
      n.sent = 0
      n.received = 0
      n.lost = 0
      n.busy = 0
      n.loss_percent = 0
      n.last_ms = null
      n.min_ms = null
      n.max_ms = null
      n.avg_ms = null
      n.jitter_ms = null
      n.samples = []
      n._sumMs = 0
      n._lastValidMs = null
      n._sumJitter = 0
      n._jitterCount = 0
    }
  }

  function recalcSnapshotCounts(snap: TaskSnapshot) {
    let completed = 0
    let overloaded = 0
    for (const r of Object.values(snap.results)) {
      if (['completed', 'timeout', 'error', 'overloaded', 'expired', 'cancelled'].includes(r.status)) {
        completed++
      }
      if (r.status === 'overloaded') {
        overloaded++
      }
    }
    snap.completed_nodes = completed
    snap.overloaded_nodes = overloaded
  }

  const resultsList = computed<NodeTaskResult[]>(() => {
    if (!snapshot.value) return []
    return Object.values(snapshot.value.results)
  })

  onUnmounted(() => {
    stopContinuousLoop()
    cleanupStream()
  })

  return {
    mode,
    snapshot,
    continuousSession,
    resultsList,
    running,
    continuous,
    errorMsg,
    continuousSamples,
    startSingle,
    startContinuous,
    pauseContinuous,
    resumeContinuous,
    clearContinuous,
    stopAll
  }
}
