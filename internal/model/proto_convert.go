package model

import agentv1 "github.com/11dog/11dog/gen/agent/v1"

// ProbeTypeToProto converts internal ProbeType to protobuf ProbeType.
func ProbeTypeToProto(t ProbeType) agentv1.ProbeType {
	switch t {
	case ProbePing:
		return agentv1.ProbeType_PROBE_TYPE_PING
	case ProbeTcping:
		return agentv1.ProbeType_PROBE_TYPE_TCPING
	case ProbeHTTP:
		return agentv1.ProbeType_PROBE_TYPE_HTTP
	case ProbeTraceroute:
		return agentv1.ProbeType_PROBE_TYPE_TRACEROUTE
	case ProbeDNS:
		return agentv1.ProbeType_PROBE_TYPE_DNS
	default:
		return agentv1.ProbeType_PROBE_TYPE_UNSPECIFIED
	}
}

// ProbeTypeFromProto converts protobuf ProbeType to internal ProbeType.
func ProbeTypeFromProto(t agentv1.ProbeType) ProbeType {
	switch t {
	case agentv1.ProbeType_PROBE_TYPE_PING:
		return ProbePing
	case agentv1.ProbeType_PROBE_TYPE_TCPING:
		return ProbeTcping
	case agentv1.ProbeType_PROBE_TYPE_HTTP:
		return ProbeHTTP
	case agentv1.ProbeType_PROBE_TYPE_TRACEROUTE:
		return ProbeTraceroute
	case agentv1.ProbeType_PROBE_TYPE_DNS:
		return ProbeDNS
	default:
		return ""
	}
}

// TaskStatusToProto converts internal TaskStatus to protobuf TaskStatus.
func TaskStatusToProto(s TaskStatus) agentv1.TaskStatus {
	switch s {
	case StatusQueued:
		return agentv1.TaskStatus_TASK_STATUS_QUEUED
	case StatusRunning:
		return agentv1.TaskStatus_TASK_STATUS_RUNNING
	case StatusCompleted:
		return agentv1.TaskStatus_TASK_STATUS_COMPLETED
	case StatusTimeout:
		return agentv1.TaskStatus_TASK_STATUS_TIMEOUT
	case StatusError:
		return agentv1.TaskStatus_TASK_STATUS_ERROR
	case StatusOverloaded:
		return agentv1.TaskStatus_TASK_STATUS_OVERLOADED
	case StatusExpired:
		return agentv1.TaskStatus_TASK_STATUS_EXPIRED
	case StatusCancelled:
		return agentv1.TaskStatus_TASK_STATUS_CANCELLED
	default:
		return agentv1.TaskStatus_TASK_STATUS_UNSPECIFIED
	}
}

// TaskStatusFromProto converts protobuf TaskStatus to internal TaskStatus.
func TaskStatusFromProto(s agentv1.TaskStatus) TaskStatus {
	switch s {
	case agentv1.TaskStatus_TASK_STATUS_QUEUED:
		return StatusQueued
	case agentv1.TaskStatus_TASK_STATUS_RUNNING:
		return StatusRunning
	case agentv1.TaskStatus_TASK_STATUS_COMPLETED:
		return StatusCompleted
	case agentv1.TaskStatus_TASK_STATUS_TIMEOUT:
		return StatusTimeout
	case agentv1.TaskStatus_TASK_STATUS_ERROR:
		return StatusError
	case agentv1.TaskStatus_TASK_STATUS_OVERLOADED:
		return StatusOverloaded
	case agentv1.TaskStatus_TASK_STATUS_EXPIRED:
		return StatusExpired
	case agentv1.TaskStatus_TASK_STATUS_CANCELLED:
		return StatusCancelled
	default:
		return StatusError
	}
}

// ProbeParamsToProto converts ProbeParams into protobuf ProbeParams based on ProbeType.
func ProbeParamsToProto(t ProbeType, p ProbeParams) *agentv1.ProbeParams {
	switch t {
	case ProbePing:
		src := p.Ping
		if src == nil {
			src = &PingParams{}
		}
		return &agentv1.ProbeParams{
			Params: &agentv1.ProbeParams_Ping{
				Ping: &agentv1.PingParams{
					Count:      src.Count,
					IntervalMs: src.IntervalMs,
					PacketSize: src.PacketSize,
					IpVersion:  src.IPVersion,
				},
			},
		}
	case ProbeTcping:
		src := p.Tcping
		if src == nil {
			src = &TcpingParams{}
		}
		return &agentv1.ProbeParams{
			Params: &agentv1.ProbeParams_Tcping{
				Tcping: &agentv1.TcpingParams{
					Port:       src.Port,
					Count:      src.Count,
					IntervalMs: src.IntervalMs,
					IpVersion:  src.IPVersion,
				},
			},
		}
	case ProbeHTTP:
		src := p.HTTP
		if src == nil {
			src = &HTTPParams{}
		}
		return &agentv1.ProbeParams{
			Params: &agentv1.ProbeParams_Http{
				Http: &agentv1.HttpParams{
					Method:             src.Method,
					HostHeader:         src.HostHeader,
					Headers:            src.Headers,
					FollowRedirects:    src.FollowRedirects,
					InsecureSkipVerify: src.InsecureSkipVerify,
					IpVersion:          src.IPVersion,
				},
			},
		}
	case ProbeTraceroute:
		src := p.Traceroute
		if src == nil {
			src = &TracerouteParams{}
		}
		return &agentv1.ProbeParams{
			Params: &agentv1.ProbeParams_Traceroute{
				Traceroute: &agentv1.TracerouteParams{
					MaxHops:       src.MaxHops,
					QueriesPerHop: src.QueriesPerHop,
					Protocol:      src.Protocol,
					IpVersion:     src.IPVersion,
				},
			},
		}
	case ProbeDNS:
		src := p.DNS
		if src == nil {
			src = &DNSParams{}
		}
		return &agentv1.ProbeParams{
			Params: &agentv1.ProbeParams_Dns{
				Dns: &agentv1.DnsParams{
					RecordType: src.RecordType,
					DnsServer:  src.DNSServer,
				},
			},
		}
	default:
		return nil
	}
}

// ProbeParamsFromProto converts protobuf ProbeParams to internal ProbeParams.
func ProbeParamsFromProto(p *agentv1.ProbeParams) ProbeParams {
	if p == nil {
		return ProbeParams{}
	}
	var out ProbeParams
	switch v := p.Params.(type) {
	case *agentv1.ProbeParams_Ping:
		out.Ping = &PingParams{
			Count:      v.Ping.Count,
			IntervalMs: v.Ping.IntervalMs,
			PacketSize: v.Ping.PacketSize,
			IPVersion:  v.Ping.IpVersion,
		}
	case *agentv1.ProbeParams_Tcping:
		out.Tcping = &TcpingParams{
			Port:       v.Tcping.Port,
			Count:      v.Tcping.Count,
			IntervalMs: v.Tcping.IntervalMs,
			IPVersion:  v.Tcping.IpVersion,
		}
	case *agentv1.ProbeParams_Http:
		out.HTTP = &HTTPParams{
			Method:             v.Http.Method,
			HostHeader:         v.Http.HostHeader,
			Headers:            v.Http.Headers,
			FollowRedirects:    v.Http.FollowRedirects,
			InsecureSkipVerify: v.Http.InsecureSkipVerify,
			IPVersion:          v.Http.IpVersion,
		}
	case *agentv1.ProbeParams_Traceroute:
		out.Traceroute = &TracerouteParams{
			MaxHops:       v.Traceroute.MaxHops,
			QueriesPerHop: v.Traceroute.QueriesPerHop,
			Protocol:      v.Traceroute.Protocol,
			IPVersion:     v.Traceroute.IpVersion,
		}
	case *agentv1.ProbeParams_Dns:
		out.DNS = &DNSParams{
			RecordType: v.Dns.RecordType,
			DNSServer:  v.Dns.DnsServer,
		}
	}
	return out
}

// ProgressFromProto converts a protobuf TaskProgress to internal ProgressPayload.
func ProgressFromProto(p *agentv1.TaskProgress) *ProgressPayload {
	if p == nil {
		return nil
	}
	out := &ProgressPayload{}
	switch v := p.Progress.(type) {
	case *agentv1.TaskProgress_PingPacket:
		if v.PingPacket != nil {
			out.PingPacket = &PingPacket{
				Seq:     v.PingPacket.Seq,
				RTTMs:   v.PingPacket.RttMs,
				TTL:     v.PingPacket.Ttl,
				Timeout: v.PingPacket.Timeout,
				ReplyIP: v.PingPacket.ReplyIp,
			}
		}
	case *agentv1.TaskProgress_TracerouteHop:
		if v.TracerouteHop != nil {
			out.TracerouteHop = &TracerouteHop{
				Hop:         v.TracerouteHop.Hop,
				IP:          v.TracerouteHop.Ip,
				Hostname:    v.TracerouteHop.Hostname,
				RTTsMs:      append([]float64(nil), v.TracerouteHop.RttsMs...),
				AvgRTTMs:    v.TracerouteHop.AvgRttMs,
				LossPercent: v.TracerouteHop.LossPercent,
				Timeout:     v.TracerouteHop.Timeout,
			}
		}
	}
	return out
}

// TaskResultFromProto converts a protobuf TaskResult to internal NodeTaskResult.
func TaskResultFromProto(r *agentv1.TaskResult) *NodeTaskResult {
	if r == nil {
		return nil
	}
	out := &NodeTaskResult{
		TaskID:         r.TaskId,
		SubtaskID:      r.SubtaskId,
		NodeID:         r.NodeId,
		Type:           ProbeTypeFromProto(r.Type),
		Status:         TaskStatusFromProto(r.Status),
		ErrorMessage:   r.ErrorMessage,
		StartedUnixMs:  r.StartedUnixMs,
		FinishedUnixMs: r.FinishedUnixMs,
		ResolvedIP:     r.ResolvedIp,
	}
	switch v := r.Result.(type) {
	case *agentv1.TaskResult_Ping:
		if v.Ping != nil {
			pkts := make([]PingPacket, 0, len(v.Ping.Packets))
			for _, pkt := range v.Ping.Packets {
				pkts = append(pkts, PingPacket{
					Seq:     pkt.Seq,
					RTTMs:   pkt.RttMs,
					TTL:     pkt.Ttl,
					Timeout: pkt.Timeout,
					ReplyIP: pkt.ReplyIp,
				})
			}
			out.Ping = &PingResult{
				Sent:        v.Ping.Sent,
				Received:    v.Ping.Received,
				LossPercent: v.Ping.LossPercent,
				MinRTTMs:    v.Ping.MinRttMs,
				AvgRTTMs:    v.Ping.AvgRttMs,
				MaxRTTMs:    v.Ping.MaxRttMs,
				MdevRTTMs:   v.Ping.MdevRttMs,
				TTL:         v.Ping.Ttl,
				Packets:     pkts,
			}
		}
	case *agentv1.TaskResult_Tcping:
		if v.Tcping != nil {
			out.Tcping = &TcpingResult{
				Port:        v.Tcping.Port,
				Sent:        v.Tcping.Sent,
				Connected:   v.Tcping.Connected,
				LossPercent: v.Tcping.LossPercent,
				MinRTTMs:    v.Tcping.MinRttMs,
				AvgRTTMs:    v.Tcping.AvgRttMs,
				MaxRTTMs:    v.Tcping.MaxRttMs,
				State:       v.Tcping.State,
				RTTsMs:      append([]float64(nil), v.Tcping.RttsMs...),
			}
		}
	case *agentv1.TaskResult_Http:
		if v.Http != nil {
			hr := &HTTPResult{
				StatusCode:    v.Http.StatusCode,
				StatusText:    v.Http.StatusText,
				FinalURL:      v.Http.FinalUrl,
				ContentLength: v.Http.ContentLength,
				RedirectCount: v.Http.RedirectCount,
				TLSVersion:    v.Http.TlsVersion,
				TLSSubject:    v.Http.TlsSubject,
			}
			if v.Http.Timing != nil {
				hr.Timing = &HTTPTiming{
					DNSMs:      v.Http.Timing.DnsMs,
					TCPMs:      v.Http.Timing.TcpMs,
					TLSMs:      v.Http.Timing.TlsMs,
					TTFBMs:     v.Http.Timing.TtfbMs,
					DownloadMs: v.Http.Timing.DownloadMs,
					TotalMs:    v.Http.Timing.TotalMs,
				}
			}
			out.HTTP = hr
		}
	case *agentv1.TaskResult_Traceroute:
		if v.Traceroute != nil {
			hops := make([]TracerouteHop, 0, len(v.Traceroute.Hops))
			for _, h := range v.Traceroute.Hops {
				hops = append(hops, TracerouteHop{
					Hop:         h.Hop,
					IP:          h.Ip,
					Hostname:    h.Hostname,
					RTTsMs:      append([]float64(nil), h.RttsMs...),
					AvgRTTMs:    h.AvgRttMs,
					LossPercent: h.LossPercent,
					Timeout:     h.Timeout,
				})
			}
			out.Traceroute = &TracerouteResult{
				Reached: v.Traceroute.Reached,
				Hops:    hops,
			}
		}
	case *agentv1.TaskResult_Dns:
		if v.Dns != nil {
			recs := make([]DNSRecord, 0, len(v.Dns.Records))
			for _, rec := range v.Dns.Records {
				recs = append(recs, DNSRecord{
					Name:  rec.Name,
					Type:  rec.Type,
					TTL:   rec.Ttl,
					Value: rec.Value,
				})
			}
			out.DNS = &DNSResult{
				ServerUsed:  v.Dns.ServerUsed,
				QueryTimeMs: v.Dns.QueryTimeMs,
				Rcode:       v.Dns.Rcode,
				Records:     recs,
			}
		}
	}
	return out
}

// TaskResultToProto converts internal NodeTaskResult back to protobuf TaskResult.
func TaskResultToProto(r *NodeTaskResult) *agentv1.TaskResult {
	if r == nil {
		return nil
	}
	out := &agentv1.TaskResult{
		TaskId:         r.TaskID,
		SubtaskId:      r.SubtaskID,
		NodeId:         r.NodeID,
		Type:           ProbeTypeToProto(r.Type),
		Status:         TaskStatusToProto(r.Status),
		ErrorMessage:   r.ErrorMessage,
		StartedUnixMs:  r.StartedUnixMs,
		FinishedUnixMs: r.FinishedUnixMs,
		ResolvedIp:     r.ResolvedIP,
	}
	switch {
	case r.Ping != nil:
		pkts := make([]*agentv1.PingPacket, 0, len(r.Ping.Packets))
		for _, pkt := range r.Ping.Packets {
			pkts = append(pkts, &agentv1.PingPacket{
				Seq:     pkt.Seq,
				RttMs:   pkt.RTTMs,
				Ttl:     pkt.TTL,
				Timeout: pkt.Timeout,
				ReplyIp: pkt.ReplyIP,
			})
		}
		out.Result = &agentv1.TaskResult_Ping{
			Ping: &agentv1.PingResult{
				Sent:        r.Ping.Sent,
				Received:    r.Ping.Received,
				LossPercent: r.Ping.LossPercent,
				MinRttMs:    r.Ping.MinRTTMs,
				AvgRttMs:    r.Ping.AvgRTTMs,
				MaxRttMs:    r.Ping.MaxRTTMs,
				MdevRttMs:   r.Ping.MdevRTTMs,
				Ttl:         r.Ping.TTL,
				Packets:     pkts,
			},
		}
	case r.Tcping != nil:
		out.Result = &agentv1.TaskResult_Tcping{
			Tcping: &agentv1.TcpingResult{
				Port:        r.Tcping.Port,
				Sent:        r.Tcping.Sent,
				Connected:   r.Tcping.Connected,
				LossPercent: r.Tcping.LossPercent,
				MinRttMs:    r.Tcping.MinRTTMs,
				AvgRttMs:    r.Tcping.AvgRTTMs,
				MaxRttMs:    r.Tcping.MaxRTTMs,
				State:       r.Tcping.State,
				RttsMs:      r.Tcping.RTTsMs,
			},
		}
	case r.HTTP != nil:
		hr := &agentv1.HttpResult{
			StatusCode:    r.HTTP.StatusCode,
			StatusText:    r.HTTP.StatusText,
			FinalUrl:      r.HTTP.FinalURL,
			ContentLength: r.HTTP.ContentLength,
			RedirectCount: r.HTTP.RedirectCount,
			TlsVersion:    r.HTTP.TLSVersion,
			TlsSubject:    r.HTTP.TLSSubject,
		}
		if r.HTTP.Timing != nil {
			hr.Timing = &agentv1.HttpTiming{
				DnsMs:      r.HTTP.Timing.DNSMs,
				TcpMs:      r.HTTP.Timing.TCPMs,
				TlsMs:      r.HTTP.Timing.TLSMs,
				TtfbMs:     r.HTTP.Timing.TTFBMs,
				DownloadMs: r.HTTP.Timing.DownloadMs,
				TotalMs:    r.HTTP.Timing.TotalMs,
			}
		}
		out.Result = &agentv1.TaskResult_Http{Http: hr}
	case r.Traceroute != nil:
		hops := make([]*agentv1.TracerouteHop, 0, len(r.Traceroute.Hops))
		for _, h := range r.Traceroute.Hops {
			hops = append(hops, &agentv1.TracerouteHop{
				Hop:         h.Hop,
				Ip:          h.IP,
				Hostname:    h.Hostname,
				RttsMs:      h.RTTsMs,
				AvgRttMs:    h.AvgRTTMs,
				LossPercent: h.LossPercent,
				Timeout:     h.Timeout,
			})
		}
		out.Result = &agentv1.TaskResult_Traceroute{
			Traceroute: &agentv1.TracerouteResult{
				Reached: r.Traceroute.Reached,
				Hops:    hops,
			},
		}
	case r.DNS != nil:
		recs := make([]*agentv1.DnsRecord, 0, len(r.DNS.Records))
		for _, rec := range r.DNS.Records {
			recs = append(recs, &agentv1.DnsRecord{
				Name:  rec.Name,
				Type:  rec.Type,
				Ttl:   rec.TTL,
				Value: rec.Value,
			})
		}
		out.Result = &agentv1.TaskResult_Dns{
			Dns: &agentv1.DnsResult{
				ServerUsed:  r.DNS.ServerUsed,
				QueryTimeMs: r.DNS.QueryTimeMs,
				Rcode:       r.DNS.Rcode,
				Records:     recs,
			},
		}
	}
	return out
}
