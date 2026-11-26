package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/dn-11/11dog/internal/model"
	"github.com/miekg/dns"
)

func runDNS(ctx context.Context, target string, p model.DNSParams) (*model.DNSResult, model.TaskStatus, error) {
	qtypeStr := strings.ToUpper(strings.TrimSpace(p.RecordType))
	if qtypeStr == "" {
		qtypeStr = "A"
	}
	qtype, ok := dns.StringToType[qtypeStr]
	if !ok {
		return nil, model.StatusError, fmt.Errorf("unsupported dns record type %q", qtypeStr)
	}

	qname := extractHost(target)
	if qtype == dns.TypePTR {
		if _, err := netip.ParseAddr(qname); err == nil {
			if rev, rerr := dns.ReverseAddr(qname); rerr == nil {
				qname = rev
			}
		}
	}
	qname = dns.Fqdn(qname)

	server := resolveDNSServer(p.DNSServer)
	msg := new(dns.Msg)
	msg.SetQuestion(qname, qtype)
	msg.RecursionDesired = true

	client := &dns.Client{
		Net:     "udp",
		Timeout: 5 * time.Second,
	}

	start := time.Now()
	resp, rtt, err := client.ExchangeContext(ctx, msg, server)
	if err == nil && resp != nil && resp.Truncated {
		tcpClient := &dns.Client{Net: "tcp", Timeout: 5 * time.Second}
		resp, rtt, err = tcpClient.ExchangeContext(ctx, msg, server)
	}
	if err != nil {
		status := model.StatusError
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = model.StatusTimeout
		} else if errors.Is(ctx.Err(), context.Canceled) {
			status = model.StatusCancelled
		}
		return &model.DNSResult{
			ServerUsed:  server,
			QueryTimeMs: durMs(time.Since(start)),
		}, status, err
	}

	records := make([]model.DNSRecord, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		hdr := rr.Header()
		records = append(records, model.DNSRecord{
			Name:  hdr.Name,
			Type:  dns.TypeToString[hdr.Rrtype],
			TTL:   hdr.Ttl,
			Value: extractRRValue(rr),
		})
	}

	return &model.DNSResult{
		ServerUsed:  server,
		QueryTimeMs: durMs(rtt),
		Rcode:       dns.RcodeToString[resp.Rcode],
		Records:     records,
	}, model.StatusCompleted, nil
}

func resolveDNSServer(override string) string {
	s := strings.TrimSpace(override)
	if s != "" {
		if _, _, err := net.SplitHostPort(s); err != nil {
			return net.JoinHostPort(strings.Trim(s, "[]"), "53")
		}
		return s
	}
	return "172.16.255.53:53"
}

func extractRRValue(rr dns.RR) string {
	switch v := rr.(type) {
	case *dns.A:
		return v.A.String()
	case *dns.CNAME:
		return v.Target
	case *dns.NS:
		return v.Ns
	case *dns.PTR:
		return v.Ptr
	case *dns.MX:
		return fmt.Sprintf("%d %s", v.Preference, v.Mx)
	case *dns.TXT:
		return strings.Join(v.Txt, " ")
	case *dns.SOA:
		return fmt.Sprintf("%s %s %d %d %d %d %d", v.Ns, v.Mbox, v.Serial, v.Refresh, v.Retry, v.Expire, v.Minttl)
	default:
		parts := strings.SplitN(rr.String(), "\t", 5)
		if len(parts) == 5 {
			return parts[4]
		}
		return rr.String()
	}
}
