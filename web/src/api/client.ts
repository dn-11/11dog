import type {
  AdminBatchUpdateResponse,
  AdminCreateNodeRequest,
  AdminNodeInfo,
  AdminOverview,
  AdminTriggerUpdateRequest,
  AdminUpdateNodeRequest,
  AdminUpdateNodeResult,
  CreateTaskRequest,
  MetaInfo,
  PublicNodeInfo,
  TargetPolicy,
  TaskEvent,
  TaskSnapshot
} from '@/types/api'

async function parseError(resp: Response): Promise<string> {
  try {
    const data = (await resp.json()) as { error?: string }
    if (data?.error) return data.error
  } catch {
    // ignore JSON decode error
  }
  return `请求失败 (HTTP ${resp.status})`
}

export async function fetchMeta(): Promise<MetaInfo> {
  const resp = await fetch('/api/v1/meta')
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as MetaInfo
}

export async function fetchNodes(): Promise<PublicNodeInfo[]> {
  const resp = await fetch('/api/v1/nodes')
  if (!resp.ok) throw new Error(await parseError(resp))
  const data = (await resp.json()) as { nodes: PublicNodeInfo[] }
  return data.nodes ?? []
}

export async function fetchAdminOverview(adminToken: string): Promise<AdminOverview> {
  const resp = await fetch('/api/v1/admin/overview', {
    headers: { 'X-Admin-Token': adminToken }
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as AdminOverview
}

export async function fetchAdminNodes(adminToken: string): Promise<AdminNodeInfo[]> {
  const resp = await fetch('/api/v1/admin/nodes', {
    headers: { 'X-Admin-Token': adminToken }
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  const data = (await resp.json()) as { nodes: AdminNodeInfo[] }
  return data.nodes ?? []
}

export async function disconnectAdminNode(adminToken: string, nodeID: string): Promise<void> {
  const resp = await fetch(`/api/v1/admin/nodes/${encodeURIComponent(nodeID)}/disconnect`, {
    method: 'POST',
    headers: { 'X-Admin-Token': adminToken }
  })
  if (!resp.ok) throw new Error(await parseError(resp))
}

export async function createAdminNode(
  adminToken: string,
  req: AdminCreateNodeRequest
): Promise<AdminNodeInfo> {
  const resp = await fetch('/api/v1/admin/nodes', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Token': adminToken
    },
    body: JSON.stringify(req)
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as AdminNodeInfo
}

export async function updateAdminNode(
  adminToken: string,
  nodeID: string,
  req: AdminUpdateNodeRequest
): Promise<AdminNodeInfo> {
  const resp = await fetch(`/api/v1/admin/nodes/${encodeURIComponent(nodeID)}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Token': adminToken
    },
    body: JSON.stringify(req)
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as AdminNodeInfo
}

export async function rotateAdminNodeToken(adminToken: string, nodeID: string): Promise<AdminNodeInfo> {
  const resp = await fetch(`/api/v1/admin/nodes/${encodeURIComponent(nodeID)}/rotate-token`, {
    method: 'POST',
    headers: { 'X-Admin-Token': adminToken }
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as AdminNodeInfo
}

export async function deleteAdminNode(adminToken: string, nodeID: string): Promise<void> {
  const resp = await fetch(`/api/v1/admin/nodes/${encodeURIComponent(nodeID)}`, {
    method: 'DELETE',
    headers: { 'X-Admin-Token': adminToken }
  })
  if (!resp.ok) throw new Error(await parseError(resp))
}

export async function triggerAdminNodeUpdate(
  adminToken: string,
  nodeID: string,
  req: AdminTriggerUpdateRequest = {}
): Promise<AdminUpdateNodeResult> {
  const resp = await fetch(`/api/v1/admin/nodes/${encodeURIComponent(nodeID)}/update`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Token': adminToken
    },
    body: JSON.stringify(req)
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as AdminUpdateNodeResult
}

export async function triggerAdminBatchUpdate(
  adminToken: string,
  req: AdminTriggerUpdateRequest = {}
): Promise<AdminBatchUpdateResponse> {
  const resp = await fetch('/api/v1/admin/nodes/update', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Token': adminToken
    },
    body: JSON.stringify(req)
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as AdminBatchUpdateResponse
}

export async function fetchAdminTargetPolicy(adminToken: string): Promise<TargetPolicy> {
  const resp = await fetch('/api/v1/admin/target-policy', {
    headers: { 'X-Admin-Token': adminToken }
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as TargetPolicy
}

export async function updateAdminTargetPolicy(
  adminToken: string,
  policy: TargetPolicy
): Promise<TargetPolicy> {
  const resp = await fetch('/api/v1/admin/target-policy', {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Token': adminToken
    },
    body: JSON.stringify(policy)
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as TargetPolicy
}

export async function createTask(req: CreateTaskRequest): Promise<TaskSnapshot> {
  const resp = await fetch('/api/v1/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req)
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as TaskSnapshot
}

export async function fetchTask(taskID: string): Promise<TaskSnapshot> {
  const resp = await fetch(`/api/v1/tasks/${encodeURIComponent(taskID)}`)
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as TaskSnapshot
}

export async function cancelTask(taskID: string): Promise<TaskSnapshot> {
  const resp = await fetch(`/api/v1/tasks/${encodeURIComponent(taskID)}/cancel`, {
    method: 'POST'
  })
  if (!resp.ok) throw new Error(await parseError(resp))
  return (await resp.json()) as TaskSnapshot
}

export interface StreamCallbacks {
  onSnapshot: (snap: TaskSnapshot) => void
  onProgress: (ev: TaskEvent) => void
  onResult: (ev: TaskEvent) => void
  onDone: (snap: TaskSnapshot) => void
  onError?: (err: string) => void
}

export function subscribeTaskStream(taskID: string, cb: StreamCallbacks): () => void {
  const es = new EventSource(`/api/v1/tasks/${encodeURIComponent(taskID)}/stream`)
  let finished = false

  es.addEventListener('snapshot', (ev) => {
    try {
      cb.onSnapshot(JSON.parse((ev as MessageEvent).data) as TaskSnapshot)
    } catch {
      // ignore malformed event
    }
  })

  es.addEventListener('progress', (ev) => {
    try {
      cb.onProgress(JSON.parse((ev as MessageEvent).data) as TaskEvent)
    } catch {
      // ignore
    }
  })

  es.addEventListener('result', (ev) => {
    try {
      cb.onResult(JSON.parse((ev as MessageEvent).data) as TaskEvent)
    } catch {
      // ignore
    }
  })

  es.addEventListener('done', (ev) => {
    finished = true
    try {
      const payload = JSON.parse((ev as MessageEvent).data) as TaskSnapshot & { snapshot?: TaskSnapshot }
      cb.onDone(payload.snapshot ?? payload)
    } catch {
      // ignore
    }
    es.close()
  })

  es.onerror = () => {
    if (!finished && es.readyState === EventSource.CLOSED) {
      cb.onError?.('实时结果流连接中断')
    }
  }

  return () => {
    finished = true
    es.close()
  }
}
