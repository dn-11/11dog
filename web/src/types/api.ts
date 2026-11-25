export type ProbeType = 'ping' | 'tcping' | 'http' | 'traceroute' | 'dns'

export type TaskStatus =
  | 'queued'
  | 'running'
  | 'completed'
  | 'timeout'
  | 'error'
  | 'overloaded'
  | 'expired'
  | 'cancelled'

export interface DN11Match {
  asn: number
  owner: string
  region?: string
  prefix?: string
}

export interface PublicNodeInfo {
  id: string
  name: string
  region: string
  group: string
  asn: number
  capabilities: ProbeType[]
  status: 'online' | 'offline'
}

export interface MetaInfo {
  site_title: string
  network_name: string
  online_nodes: number
}

export interface AgentUpdateState {
  target_version?: string
  state: 'pending' | 'downloading' | 'verifying' | 'applying' | 'restarting' | 'up_to_date' | 'failed' | string
  message?: string
  updated_at_ms?: number
}

export interface AdminNodeInfo {
  id: string
  name: string
  region: string
  group: string
  asn: number
  ipv4?: string
  ipv6?: string
  capabilities: ProbeType[]
  max_concurrency: number
  active_tasks: number
  queue_depth: number
  cpu_percent: number
  mem_percent: number
  version: string
  os?: string
  arch?: string
  update_status?: AgentUpdateState
  status: 'online' | 'offline'
  enabled: boolean
  notes?: string
  token?: string
  creds?: string
  created_at_ms?: number
  updated_at_ms?: number
  last_seen_unix_ms: number
  connected_at_unix_ms?: number
  labels?: Record<string, string>
}

export interface AdminTriggerUpdateRequest {
  node_ids?: string[]
  version?: string
  download_url?: string
  sha256?: string
  force?: boolean
}

export interface AdminUpdateNodeResult {
  node_id: string
  sent: boolean
  message?: string
}

export interface AdminBatchUpdateResponse {
  results: AdminUpdateNodeResult[]
  total: number
  sent: number
}

export interface TargetPolicy {
  enabled: boolean
  allow_rules: string[]
  deny_rules: string[]
  updated_at_ms?: number
}

export interface AdminCreateNodeRequest {
  id?: string
  name: string
  region: string
  group: string
  asn: number
  max_concurrency: number
  notes?: string
}

export interface AdminUpdateNodeRequest {
  name?: string
  region?: string
  group?: string
  asn?: number
  max_concurrency?: number
  enabled?: boolean
  notes?: string
}

export interface AdminOverview {
  version: string
  db_driver: string
  nats_url: string
  public_nats_url?: string
  nats_account_pub?: string
  max_queue_per_node: number
  agent_download_base_url?: string
  hosted_binaries: string[]
  online_nodes: number
  offline_nodes: number
  total_nodes: number
  total_capacity: number
  active_tasks: number
  total_queue_depth: number
  tasks_created_total: number
  subtasks_enqueued_total: number
  subtasks_overloaded_total: number
  subtasks_expired_total: number
  subtasks_completed_total: number
}

export interface PingParams {
  count?: number
  interval_ms?: number
  packet_size?: number
  ip_version?: string
}

export interface TcpingParams {
  port: number
  count?: number
  interval_ms?: number
  ip_version?: string
}

export interface HTTPParams {
  method?: string
  host_header?: string
  headers?: Record<string, string>
  follow_redirects?: boolean
  insecure_skip_verify?: boolean
  ip_version?: string
}

export interface TracerouteParams {
  max_hops?: number
  queries_per_hop?: number
  protocol?: string
  ip_version?: string
}

export interface DNSParams {
  record_type?: string
  dns_server?: string
}

export interface ProbeParams {
  ping?: PingParams
  tcping?: TcpingParams
  http?: HTTPParams
  traceroute?: TracerouteParams
  dns?: DNSParams
}

export interface CreateTaskRequest {
  type: ProbeType
  target: string
  node_ids?: string[]
  regions?: string[]
  groups?: string[]
  timeout_ms?: number
  params?: ProbeParams
}

export interface PingPacket {
  seq: number
  rtt_ms: number
  ttl: number
  timeout: boolean
  reply_ip?: string
}

export interface PingResult {
  sent: number
  received: number
  loss_percent: number
  min_rtt_ms: number
  avg_rtt_ms: number
  max_rtt_ms: number
  mdev_rtt_ms: number
  ttl: number
  packets: PingPacket[]
}

export interface TcpingResult {
  port: number
  sent: number
  connected: number
  loss_percent: number
  min_rtt_ms: number
  avg_rtt_ms: number
  max_rtt_ms: number
  state: 'open' | 'refused' | 'timeout' | string
  rtts_ms: number[]
}

export interface HTTPTiming {
  dns_ms: number
  tcp_ms: number
  tls_ms: number
  ttfb_ms: number
  download_ms: number
  total_ms: number
}

export interface HTTPResult {
  status_code: number
  status_text: string
  final_url: string
  content_length: number
  redirect_count: number
  tls_version?: string
  tls_subject?: string
  timing?: HTTPTiming
}

export interface TracerouteHop {
  hop: number
  ip?: string
  hostname?: string
  rtts_ms: number[]
  avg_rtt_ms: number
  loss_percent: number
  timeout: boolean
  dn11?: DN11Match
}

export interface TracerouteResult {
  reached: boolean
  hops: TracerouteHop[]
}

export interface DNSRecord {
  name: string
  type: string
  ttl: number
  value: string
}

export interface DNSResult {
  server_used: string
  query_time_ms: number
  rcode: string
  records: DNSRecord[]
}

export interface NodeTaskResult {
  task_id: string
  subtask_id: string
  node_id: string
  node_name: string
  region: string
  group: string
  node_asn: number
  type: ProbeType
  status: TaskStatus
  error_message?: string
  started_unix_ms?: number
  finished_unix_ms?: number
  resolved_ip?: string
  resolved_dn11?: DN11Match
  ping?: PingResult
  tcping?: TcpingResult
  http?: HTTPResult
  traceroute?: TracerouteResult
  dns?: DNSResult
}

export interface TaskSnapshot {
  task_id: string
  type: ProbeType
  target: string
  params: ProbeParams
  created_unix_ms: number
  deadline_unix_ms: number
  cancelled: boolean
  total_nodes: number
  completed_nodes: number
  overloaded_nodes: number
  results: Record<string, NodeTaskResult>
}

export interface TaskEvent {
  event_type: 'snapshot' | 'progress' | 'result' | 'done'
  task_id: string
  subtask_id?: string
  node_id?: string
  type?: ProbeType
  status?: TaskStatus
  progress?: {
    ping_packet?: PingPacket
    traceroute_hop?: TracerouteHop
  }
  result?: NodeTaskResult
  snapshot?: TaskSnapshot
}
