package model

import "github.com/google/uuid"

// ProbeType enumerates supported network diagnostic probe types.
type ProbeType string

const (
	ProbePing       ProbeType = "ping"
	ProbeTcping     ProbeType = "tcping"
	ProbeHTTP       ProbeType = "http"
	ProbeTraceroute ProbeType = "traceroute"
	ProbeDNS        ProbeType = "dns"
)

// IsValid returns true when p is one of the supported probe types.
func (p ProbeType) IsValid() bool {
	switch p {
	case ProbePing, ProbeTcping, ProbeHTTP, ProbeTraceroute, ProbeDNS:
		return true
	default:
		return false
	}
}

// TaskStatus represents the lifecycle state of a node subtask.
type TaskStatus string

const (
	StatusQueued     TaskStatus = "queued"
	StatusRunning    TaskStatus = "running"
	StatusCompleted  TaskStatus = "completed"
	StatusTimeout    TaskStatus = "timeout"
	StatusError      TaskStatus = "error"
	StatusOverloaded TaskStatus = "overloaded"
	StatusExpired    TaskStatus = "expired"
	StatusCancelled  TaskStatus = "cancelled"
)

// IsTerminal reports whether the task status will receive no further updates.
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusTimeout, StatusError, StatusOverloaded, StatusExpired, StatusCancelled:
		return true
	default:
		return false
	}
}

// DN11Match describes DN11 network ownership metadata for a resolved or hop IP.
type DN11Match struct {
	ASN    uint32 `json:"asn"`
	Owner  string `json:"owner"`
	Region string `json:"region,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// AgentUpdateState tracks the latest self-update progress reported by an edge agent.
type AgentUpdateState struct {
	TargetVersion     string `json:"target_version,omitempty"`
	PreviousVersion   string `json:"previous_version,omitempty"`
	PrevConnectedAtMs int64  `json:"prev_connected_at_ms,omitempty"`
	State             string `json:"state"` // pending | downloading | verifying | applying | restarting | up_to_date | failed
	Message           string `json:"message,omitempty"`
	UpdatedAtMs       int64  `json:"updated_at_ms,omitempty"`
}

// NodeInfo describes an edge probe node and its live internal telemetry (admin only).
type NodeInfo struct {
	ID             uuid.UUID         `json:"id"`
	Name           string            `json:"name"`
	Region         string            `json:"region"`
	Group          string            `json:"group"`
	ASN            uint32            `json:"asn"`
	Capabilities   []ProbeType       `json:"capabilities"`
	MaxConcurrency uint32            `json:"max_concurrency"`
	ActiveTasks    uint32            `json:"active_tasks"`
	QueueDepth     int               `json:"queue_depth"`
	Version        string            `json:"version"`
	OS             string            `json:"os,omitempty"`
	Arch           string            `json:"arch,omitempty"`
	UpdateStatus   *AgentUpdateState `json:"update_status,omitempty"`
	Status         string            `json:"status"` // online | offline
	Enabled        bool              `json:"enabled"`
	Notes          string            `json:"notes,omitempty"`
	Token          string            `json:"token,omitempty"`
	Creds          string            `json:"creds,omitempty"`
	CreatedAtMs    int64             `json:"created_at_ms,omitempty"`
	UpdatedAtMs    int64             `json:"updated_at_ms,omitempty"`
	LastSeenUnixMs int64             `json:"last_seen_unix_ms"`
	ConnectedAtMs  int64             `json:"connected_at_unix_ms,omitempty"`
}

// PublicNodeInfo exposes only safe user-facing probe location attributes.
type PublicNodeInfo struct {
	ID           uuid.UUID   `json:"id"`
	Name         string      `json:"name"`
	Region       string      `json:"region"`
	Group        string      `json:"group"`
	ASN          uint32      `json:"asn"`
	Capabilities []ProbeType `json:"capabilities"`
	Status       string      `json:"status"`
}

// AdminCreateNodeRequest creates an authoritative node record and communication credentials in DB.
type AdminCreateNodeRequest struct {
	Name           string `json:"name"`
	Region         string `json:"region"`
	Group          string `json:"group"`
	ASN            uint32 `json:"asn"`
	MaxConcurrency uint32 `json:"max_concurrency"`
	Notes          string `json:"notes,omitempty"`
}

// AdminUpdateNodeRequest modifies an existing node record in DB.
type AdminUpdateNodeRequest struct {
	Name           *string `json:"name,omitempty"`
	Region         *string `json:"region,omitempty"`
	Group          *string `json:"group,omitempty"`
	ASN            *uint32 `json:"asn,omitempty"`
	MaxConcurrency *uint32 `json:"max_concurrency,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
	Notes          *string `json:"notes,omitempty"`
}

// AdminTriggerUpdateRequest dispatches a self-update command to one or more online agents.
type AdminTriggerUpdateRequest struct {
	NodeIDs     []uuid.UUID `json:"node_ids,omitempty"`
	Version     string      `json:"version,omitempty"`
	DownloadURL string      `json:"download_url,omitempty"`
	SHA256      string      `json:"sha256,omitempty"`
	Force       bool        `json:"force,omitempty"`
}

// AdminUpdateNodeResult reports the dispatch result for a single node update command.
type AdminUpdateNodeResult struct {
	NodeID  uuid.UUID `json:"node_id"`
	Sent    bool      `json:"sent"`
	Message string    `json:"message,omitempty"`
}

// TargetPolicy configures global probe target restrictions supporting domain wildcards and CIDRs.
type TargetPolicy struct {
	Enabled     bool     `json:"enabled"`
	AllowRules  []string `json:"allow_rules"`
	DenyRules   []string `json:"deny_rules"`
	UpdatedAtMs int64    `json:"updated_at_ms,omitempty"`
}

// AdminOverview aggregates internal server state, DB/NATS telemetry, and counters.
type AdminOverview struct {
	Version                string   `json:"version"`
	DBDriver               string   `json:"db_driver"`
	NATSURL                string   `json:"nats_url"`
	PublicNATSURL          string   `json:"public_nats_url,omitempty"`
	NATSAccountPub         string   `json:"nats_account_pub,omitempty"`
	MaxQueuePerNode        int      `json:"max_queue_per_node"`
	AgentDownloadBaseURL   string   `json:"agent_download_base_url,omitempty"`
	HostedBinaries         []string `json:"hosted_binaries"`
	OnlineNodes            int      `json:"online_nodes"`
	OfflineNodes           int      `json:"offline_nodes"`
	TotalNodes             int      `json:"total_nodes"`
	TotalCapacity          uint32   `json:"total_capacity"`
	ActiveTasks            uint32   `json:"active_tasks"`
	TotalQueueDepth        int      `json:"total_queue_depth"`
	TasksCreatedTotal      int64    `json:"tasks_created_total"`
	SubtasksEnqueuedTotal  int64    `json:"subtasks_enqueued_total"`
	SubtasksOverloadTotal  int64    `json:"subtasks_overloaded_total"`
	SubtasksExpiredTotal   int64    `json:"subtasks_expired_total"`
	SubtasksCompletedTotal int64    `json:"subtasks_completed_total"`
}

// AgentHeartbeat is published periodically by edge agents over Core NATS (agent.hb.<nodeID>).
type AgentHeartbeat struct {
	NodeID          uuid.UUID         `json:"node_id"`
	Capabilities    []ProbeType       `json:"capabilities,omitempty"`
	MaxConcurrency  uint32            `json:"max_concurrency"`
	ActiveTasks     uint32            `json:"active_tasks"`
	Version         string            `json:"version"`
	OS              string            `json:"os,omitempty"`
	Arch            string            `json:"arch,omitempty"`
	TimestampUnixMs int64       `json:"timestamp_unix_ms"`
	ConnectedAtMs   int64       `json:"connected_at_unix_ms,omitempty"`
}

// CancelTaskCommand is published to ctrl.node.<nodeID>.cancel.
type CancelTaskCommand struct {
	TaskID    string `json:"task_id"`
	SubtaskID string `json:"subtask_id"`
	Reason    string `json:"reason,omitempty"`
}

// AgentUpdateCommand is published to ctrl.node.<nodeID>.update.
type AgentUpdateCommand struct {
	Version     string `json:"version,omitempty"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256,omitempty"`
	Force       bool   `json:"force,omitempty"`
}

// ReconnectCommand is published to ctrl.node.<nodeID>.reconnect.
type ReconnectCommand struct {
	Reason string `json:"reason,omitempty"`
}

// CreateTaskRequest is the HTTP payload to dispatch a network diagnostic task.
type CreateTaskRequest struct {
	Type      ProbeType   `json:"type"`
	Target    string      `json:"target"`
	NodeIDs   []uuid.UUID `json:"node_ids,omitempty"`
	Regions   []string    `json:"regions,omitempty"`
	Groups    []string    `json:"groups,omitempty"`
	TimeoutMs uint32      `json:"timeout_ms,omitempty"`
	Params    ProbeParams `json:"params,omitempty"`
}

// ProbeParams holds probe-specific execution options.
type ProbeParams struct {
	Ping       *PingParams       `json:"ping,omitempty"`
	Tcping     *TcpingParams     `json:"tcping,omitempty"`
	HTTP       *HTTPParams       `json:"http,omitempty"`
	Traceroute *TracerouteParams `json:"traceroute,omitempty"`
	DNS        *DNSParams        `json:"dns,omitempty"`
}

type PingParams struct {
	Count      uint32 `json:"count,omitempty"`
	IntervalMs uint32 `json:"interval_ms,omitempty"`
	PacketSize uint32 `json:"packet_size,omitempty"`
}

type TcpingParams struct {
	Port       uint32 `json:"port"`
	Count      uint32 `json:"count,omitempty"`
	IntervalMs uint32 `json:"interval_ms,omitempty"`
}

type HTTPParams struct {
	Method             string            `json:"method,omitempty"`
	HostHeader         string            `json:"host_header,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	FollowRedirects    bool              `json:"follow_redirects,omitempty"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify,omitempty"`
}

type TracerouteParams struct {
	MaxHops       uint32 `json:"max_hops,omitempty"`
	QueriesPerHop uint32 `json:"queries_per_hop,omitempty"`
}

type DNSParams struct {
	RecordType string `json:"record_type,omitempty"` // A, CNAME, MX, TXT, NS, PTR, SOA
	DNSServer  string `json:"dns_server,omitempty"`
}

type PingPacket struct {
	Seq     uint32  `json:"seq"`
	RTTMs   float64 `json:"rtt_ms"`
	TTL     uint32  `json:"ttl"`
	Timeout bool    `json:"timeout"`
	ReplyIP string  `json:"reply_ip,omitempty"`
}

type PingResult struct {
	Sent        uint32       `json:"sent"`
	Received    uint32       `json:"received"`
	LossPercent float64      `json:"loss_percent"`
	MinRTTMs    float64      `json:"min_rtt_ms"`
	AvgRTTMs    float64      `json:"avg_rtt_ms"`
	MaxRTTMs    float64      `json:"max_rtt_ms"`
	MdevRTTMs   float64      `json:"mdev_rtt_ms"`
	TTL         uint32       `json:"ttl"`
	Packets     []PingPacket `json:"packets"`
}

type TcpingResult struct {
	Port        uint32    `json:"port"`
	Sent        uint32    `json:"sent"`
	Connected   uint32    `json:"connected"`
	LossPercent float64   `json:"loss_percent"`
	MinRTTMs    float64   `json:"min_rtt_ms"`
	AvgRTTMs    float64   `json:"avg_rtt_ms"`
	MaxRTTMs    float64   `json:"max_rtt_ms"`
	State       string    `json:"state"` // open, refused, timeout
	RTTsMs      []float64 `json:"rtts_ms"`
}

type HTTPTiming struct {
	DNSMs      float64 `json:"dns_ms"`
	TCPMs      float64 `json:"tcp_ms"`
	TLSMs      float64 `json:"tls_ms"`
	TTFBMs     float64 `json:"ttfb_ms"`
	DownloadMs float64 `json:"download_ms"`
	TotalMs    float64 `json:"total_ms"`
}

type HTTPResult struct {
	StatusCode    int32       `json:"status_code"`
	StatusText    string      `json:"status_text"`
	FinalURL      string      `json:"final_url"`
	ContentLength uint64      `json:"content_length"`
	RedirectCount uint32      `json:"redirect_count"`
	TLSVersion    string      `json:"tls_version,omitempty"`
	TLSSubject    string      `json:"tls_subject,omitempty"`
	Timing        *HTTPTiming `json:"timing,omitempty"`
}

type TracerouteHop struct {
	Hop         uint32     `json:"hop"`
	IP          string     `json:"ip,omitempty"`
	Hostname    string     `json:"hostname,omitempty"`
	RTTsMs      []float64  `json:"rtts_ms"`
	AvgRTTMs    float64    `json:"avg_rtt_ms"`
	LossPercent float64    `json:"loss_percent"`
	Timeout     bool       `json:"timeout"`
	DN11        *DN11Match `json:"dn11,omitempty"`
}

type TracerouteResult struct {
	Reached bool            `json:"reached"`
	Hops    []TracerouteHop `json:"hops"`
}

type DNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	Value string `json:"value"`
}

type DNSResult struct {
	ServerUsed  string      `json:"server_used"`
	QueryTimeMs float64     `json:"query_time_ms"`
	Rcode       string      `json:"rcode"`
	Records     []DNSRecord `json:"records"`
}

// NodeTaskResult stores the probe result for one node inside a task.
type NodeTaskResult struct {
	TaskID         string            `json:"task_id"`
	SubtaskID      string            `json:"subtask_id"`
	NodeID         uuid.UUID         `json:"node_id"`
	NodeName       string            `json:"node_name"`
	Region         string            `json:"region"`
	Group          string            `json:"group"`
	NodeASN        uint32            `json:"node_asn"`
	Type           ProbeType         `json:"type"`
	Status         TaskStatus        `json:"status"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	StartedUnixMs  int64             `json:"started_unix_ms,omitempty"`
	FinishedUnixMs int64             `json:"finished_unix_ms,omitempty"`
	ResolvedIP     string            `json:"resolved_ip,omitempty"`
	ResolvedDN11   *DN11Match        `json:"resolved_dn11,omitempty"`
	Ping           *PingResult       `json:"ping,omitempty"`
	Tcping         *TcpingResult     `json:"tcping,omitempty"`
	HTTP           *HTTPResult       `json:"http,omitempty"`
	Traceroute     *TracerouteResult `json:"traceroute,omitempty"`
	DNS            *DNSResult        `json:"dns,omitempty"`
}

// TaskSnapshot represents the complete state of a multi-node task.
type TaskSnapshot struct {
	TaskID          string                        `json:"task_id"`
	Type            ProbeType                     `json:"type"`
	Target          string                        `json:"target"`
	Params          ProbeParams                   `json:"params"`
	CreatedUnixMs   int64                         `json:"created_unix_ms"`
	DeadlineUnixMs  int64                         `json:"deadline_unix_ms"`
	Cancelled       bool                          `json:"cancelled"`
	TotalNodes      int                           `json:"total_nodes"`
	CompletedNodes  int                           `json:"completed_nodes"`
	OverloadedNodes int                           `json:"overloaded_nodes"`
	Results         map[uuid.UUID]*NodeTaskResult `json:"results"`
}

// ProgressPayload wraps incremental probe updates (ping packets or traceroute hops).
type ProgressPayload struct {
	PingPacket    *PingPacket    `json:"ping_packet,omitempty"`
	TracerouteHop *TracerouteHop `json:"traceroute_hop,omitempty"`
}

// TaskEvent is published over MQ and streamed via SSE to connected clients.
type TaskEvent struct {
	EventType    string           `json:"event_type"` // snapshot, progress, result, done
	TaskID       string           `json:"task_id"`
	SubtaskID    string           `json:"subtask_id,omitempty"`
	NodeID       uuid.UUID        `json:"node_id,omitempty"`
	Type         ProbeType        `json:"type,omitempty"`
	Status       TaskStatus       `json:"status,omitempty"`
	ResolvedIP   string           `json:"resolved_ip,omitempty"`
	ResolvedDN11 *DN11Match       `json:"resolved_dn11,omitempty"`
	Progress     *ProgressPayload `json:"progress,omitempty"`
	Result       *NodeTaskResult  `json:"result,omitempty"`
	Snapshot     *TaskSnapshot    `json:"snapshot,omitempty"`
}
