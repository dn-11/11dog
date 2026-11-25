package probe

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/dn-11/11dog/internal/model"
	"github.com/google/uuid"
)

// ProgressFunc is invoked when an incremental probe update (ping packet or traceroute hop) is ready.
type ProgressFunc func(payload *model.ProgressPayload)

// Run executes a diagnostic probe according to probeType and returns the final NodeTaskResult.
func Run(
	ctx context.Context,
	taskID, subtaskID string,
	nodeID uuid.UUID,
	probeType model.ProbeType,
	target string,
	timeoutMs uint32,
	params model.ProbeParams,
	onProgress ProgressFunc,
) *model.NodeTaskResult {
	if timeoutMs == 0 {
		timeoutMs = 15000
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	started := time.Now().UnixMilli()
	res := &model.NodeTaskResult{
		TaskID:        taskID,
		SubtaskID:     subtaskID,
		NodeID:        nodeID,
		Type:          probeType,
		StartedUnixMs: started,
	}

	switch probeType {
	case model.ProbePing:
		p := model.PingParams{}
		if params.Ping != nil {
			p = *params.Ping
		}
		resolved, pingRes, status, err := runPing(probeCtx, target, p, onProgress)
		res.ResolvedIP = resolved
		res.Ping = pingRes
		res.Status = status
		if err != nil {
			res.ErrorMessage = err.Error()
		}

	case model.ProbeTcping:
		p := model.TcpingParams{}
		if params.Tcping != nil {
			p = *params.Tcping
		}
		resolved, tcpRes, status, err := runTcping(probeCtx, target, p)
		res.ResolvedIP = resolved
		res.Tcping = tcpRes
		res.Status = status
		if err != nil {
			res.ErrorMessage = err.Error()
		}

	case model.ProbeHTTP:
		p := model.HTTPParams{FollowRedirects: true}
		if params.HTTP != nil {
			p = *params.HTTP
		}
		resolved, httpRes, status, err := runHTTP(probeCtx, target, p)
		res.ResolvedIP = resolved
		res.HTTP = httpRes
		res.Status = status
		if err != nil {
			res.ErrorMessage = err.Error()
		}

	case model.ProbeTraceroute:
		p := model.TracerouteParams{}
		if params.Traceroute != nil {
			p = *params.Traceroute
		}
		resolved, traceRes, status, err := runTraceroute(probeCtx, target, p, onProgress)
		res.ResolvedIP = resolved
		res.Traceroute = traceRes
		res.Status = status
		if err != nil {
			res.ErrorMessage = err.Error()
		}

	case model.ProbeDNS:
		p := model.DNSParams{}
		if params.DNS != nil {
			p = *params.DNS
		}
		dnsRes, status, err := runDNS(probeCtx, target, p)
		res.DNS = dnsRes
		res.Status = status
		if dnsRes != nil {
			for _, rec := range dnsRes.Records {
				if rec.Type == "A" || rec.Type == "AAAA" {
					res.ResolvedIP = rec.Value
					break
				}
			}
		}
		if err != nil {
			res.ErrorMessage = err.Error()
		}

	default:
		res.Status = model.StatusError
		res.ErrorMessage = fmt.Sprintf("unsupported probe type %q", probeType)
	}

	if res.Status == "" {
		res.Status = model.StatusCompleted
	}
	res.FinishedUnixMs = time.Now().UnixMilli()
	return res
}

// extractHost cleans a user-supplied target (hostname, IP, host:port, or URL) into a bare host/IP.
func extractHost(target string) string {
	t := strings.TrimSpace(target)
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		if u, err := url.Parse(t); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	if addr, err := netip.ParseAddr(strings.Trim(t, "[]")); err == nil {
		return addr.String()
	}
	if host, _, err := net.SplitHostPort(t); err == nil && host != "" {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(t, "[]")
}

// resolveHost resolves host into a single netip.Addr respecting ipVersion ("auto", "ipv4", "ipv6").
func resolveHost(ctx context.Context, target string, ipVersion string) (netip.Addr, error) {
	host := extractHost(target)
	if host == "" {
		return netip.Addr{}, fmt.Errorf("empty target host")
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		switch strings.ToLower(ipVersion) {
		case "ipv4":
			if !addr.Is4() {
				return netip.Addr{}, fmt.Errorf("target %s is not IPv4", host)
			}
		case "ipv6":
			if !addr.Is6() {
				return netip.Addr{}, fmt.Errorf("target %s is not IPv6", host)
			}
		}
		return addr, nil
	}

	network := "ip"
	switch strings.ToLower(ipVersion) {
	case "ipv4":
		network = "ip4"
	case "ipv6":
		network = "ip6"
	}

	resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupNetIP(resolveCtx, network, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("no IP addresses found for %q", host)
	}

	// In auto mode, prefer IPv4 first if available for predictable dual-stack behavior.
	if network == "ip" {
		for _, a := range addrs {
			u := a.Unmap()
			if u.Is4() {
				return u, nil
			}
		}
	}
	return addrs[0].Unmap(), nil
}
