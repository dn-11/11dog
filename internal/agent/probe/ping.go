package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"time"

	"github.com/11dog/11dog/internal/model"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func randomID16() int {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint16(b[:]))
}

func openICMPConn(isV6 bool) (conn *icmp.PacketConn, unprivileged bool, err error) {
	if !isV6 {
		if c, e := icmp.ListenPacket("ip4:icmp", "0.0.0.0"); e == nil {
			_ = c.IPv4PacketConn().SetControlMessage(ipv4.FlagTTL, true)
			return c, false, nil
		} else if cu, eu := icmp.ListenPacket("udp4", "0.0.0.0"); eu == nil {
			_ = cu.IPv4PacketConn().SetControlMessage(ipv4.FlagTTL, true)
			return cu, true, nil
		} else {
			return nil, false, fmt.Errorf("icmp listen failed (raw: %v; udp4: %v)", e, eu)
		}
	}
	if c, e := icmp.ListenPacket("ip6:ipv6-icmp", "::"); e == nil {
		_ = c.IPv6PacketConn().SetControlMessage(ipv6.FlagHopLimit, true)
		return c, false, nil
	} else if cu, eu := icmp.ListenPacket("udp6", "::"); eu == nil {
		_ = cu.IPv6PacketConn().SetControlMessage(ipv6.FlagHopLimit, true)
		return cu, true, nil
	} else {
		return nil, false, fmt.Errorf("icmp6 listen failed (raw: %v; udp6: %v)", e, eu)
	}
}

func runPing(ctx context.Context, target string, p model.PingParams, onProgress ProgressFunc) (string, *model.PingResult, model.TaskStatus, error) {
	addr, err := resolveHost(ctx, target, p.IPVersion)
	if err != nil {
		return "", nil, model.StatusError, err
	}
	resolvedIP := addr.String()

	count := int(p.Count)
	if count <= 0 {
		count = 4
	}
	interval := time.Duration(p.IntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 300 * time.Millisecond
	}
	pktSize := int(p.PacketSize)
	if pktSize < 16 {
		pktSize = 56
	}

	isV6 := addr.Is6()
	conn, unprivileged, err := openICMPConn(isV6)
	if err != nil {
		return resolvedIP, nil, model.StatusError, err
	}
	defer conn.Close()

	var dst net.Addr
	if unprivileged {
		dst = &net.UDPAddr{IP: addr.AsSlice(), Zone: addr.Zone()}
	} else {
		dst = &net.IPAddr{IP: addr.AsSlice(), Zone: addr.Zone()}
	}

	icmpID := randomID16()
	var reqType icmp.Type = ipv4.ICMPTypeEcho
	var replyType icmp.Type = ipv4.ICMPTypeEchoReply
	protoNum := 1 // ICMPv4
	if isV6 {
		reqType = ipv6.ICMPTypeEchoRequest
		replyType = ipv6.ICMPTypeEchoReply
		protoNum = 58 // ICMPv6
	}

	payload := make([]byte, pktSize)
	for i := range payload {
		payload[i] = byte('a' + (i % 26))
	}

	packets := make([]model.PingPacket, 0, count)
	var rtts []float64
	var lastTTL uint32
	var received uint32

	buf := make([]byte, 2048)

	for seq := 1; seq <= count; seq++ {
		if ctx.Err() != nil {
			break
		}

		msg := icmp.Message{
			Type: reqType,
			Code: 0,
			Body: &icmp.Echo{
				ID:   icmpID,
				Seq:  seq,
				Data: payload,
			},
		}
		wire, err := msg.Marshal(nil)
		if err != nil {
			return resolvedIP, nil, model.StatusError, err
		}

		sendTime := time.Now()
		if _, err := conn.WriteTo(wire, dst); err != nil {
			pkt := model.PingPacket{Seq: uint32(seq), Timeout: true}
			packets = append(packets, pkt)
			if onProgress != nil {
				onProgress(&model.ProgressPayload{PingPacket: &pkt})
			}
			continue
		}

		perWait := 1500 * time.Millisecond
		if dl, ok := ctx.Deadline(); ok {
			if rem := time.Until(dl); rem < perWait {
				perWait = rem
			}
		}
		if perWait <= 0 {
			perWait = 50 * time.Millisecond
		}
		_ = conn.SetReadDeadline(time.Now().Add(perWait))

		pkt := model.PingPacket{Seq: uint32(seq), Timeout: true}
		for {
			var n int
			var ttl int
			var peer net.Addr
			if !isV6 && conn.IPv4PacketConn() != nil {
				var cm *ipv4.ControlMessage
				n, cm, peer, err = conn.IPv4PacketConn().ReadFrom(buf)
				if cm != nil {
					ttl = cm.TTL
				}
			} else if isV6 && conn.IPv6PacketConn() != nil {
				var cm *ipv6.ControlMessage
				n, cm, peer, err = conn.IPv6PacketConn().ReadFrom(buf)
				if cm != nil {
					ttl = cm.HopLimit
				}
			} else {
				n, peer, err = conn.ReadFrom(buf)
			}

			if err != nil {
				break
			}

			parsed, perr := icmp.ParseMessage(protoNum, buf[:n])
			if perr != nil || parsed.Type != replyType {
				continue
			}
			echo, ok := parsed.Body.(*icmp.Echo)
			if !ok || echo.Seq != seq {
				continue
			}
			if !unprivileged && echo.ID != icmpID {
				continue
			}

			peerHost := peerIPString(peer)
			if peerAddr, perr := netip.ParseAddr(peerHost); perr == nil && peerAddr.Unmap() != addr {
				continue
			}

			rttMs := float64(time.Since(sendTime).Microseconds()) / 1000.0
			pkt.Timeout = false
			pkt.RTTMs = math.Round(rttMs*100) / 100
			pkt.TTL = uint32(ttl)
			pkt.ReplyIP = peerHost
			if ttl > 0 {
				lastTTL = uint32(ttl)
			}
			received++
			rtts = append(rtts, pkt.RTTMs)
			break
		}

		packets = append(packets, pkt)
		if onProgress != nil {
			pktCopy := pkt
			onProgress(&model.ProgressPayload{PingPacket: &pktCopy})
		}

		if seq < count {
			select {
			case <-ctx.Done():
			case <-time.After(interval):
			}
		}
	}

	sent := uint32(len(packets))
	loss := 100.0
	if sent > 0 {
		loss = math.Round((float64(sent-received)/float64(sent))*10000) / 100
	}

	minRTT, avgRTT, maxRTT, mdevRTT := calcRTTStats(rtts)
	res := &model.PingResult{
		Sent:        sent,
		Received:    received,
		LossPercent: loss,
		MinRTTMs:    minRTT,
		AvgRTTMs:    avgRTT,
		MaxRTTMs:    maxRTT,
		MdevRTTMs:   mdevRTT,
		TTL:         lastTTL,
		Packets:     packets,
	}

	status := model.StatusCompleted
	if received == 0 && sent > 0 {
		if errors.Is(ctx.Err(), context.Canceled) {
			status = model.StatusCancelled
		} else {
			status = model.StatusTimeout
		}
	}
	return resolvedIP, res, status, nil
}

func peerIPString(a net.Addr) string {
	if a == nil {
		return ""
	}
	switch v := a.(type) {
	case *net.IPAddr:
		return v.IP.String()
	case *net.UDPAddr:
		return v.IP.String()
	default:
		host, _, err := net.SplitHostPort(a.String())
		if err == nil {
			return host
		}
		return a.String()
	}
}

func calcRTTStats(rtts []float64) (minV, avgV, maxV, mdevV float64) {
	if len(rtts) == 0 {
		return 0, 0, 0, 0
	}
	minV = rtts[0]
	maxV = rtts[0]
	var sum float64
	for _, r := range rtts {
		if r < minV {
			minV = r
		}
		if r > maxV {
			maxV = r
		}
		sum += r
	}
	avgV = sum / float64(len(rtts))
	var sqDiff float64
	for _, r := range rtts {
		d := r - avgV
		sqDiff += d * d
	}
	mdevV = math.Sqrt(sqDiff / float64(len(rtts)))
	return round2(minV), round2(avgV), round2(maxV), round2(mdevV)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
