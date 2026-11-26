package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/dn-11/11dog/internal/model"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func runTraceroute(ctx context.Context, target string, p model.TracerouteParams, onProgress ProgressFunc) (string, *model.TracerouteResult, model.TaskStatus, error) {
	addr, err := resolveHost(ctx, target)
	if err != nil {
		return "", nil, model.StatusError, err
	}
	resolvedIP := addr.String()

	maxHops := int(p.MaxHops)
	if maxHops <= 0 {
		maxHops = 30
	}
	if maxHops > 64 {
		maxHops = 64
	}
	queriesPerHop := int(p.QueriesPerHop)
	if queriesPerHop <= 0 {
		queriesPerHop = 3
	}
	if queriesPerHop > 5 {
		queriesPerHop = 5
	}

	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return resolvedIP, nil, model.StatusError, fmt.Errorf("traceroute requires raw ICMP socket (CAP_NET_RAW): %w", err)
	}
	defer conn.Close()

	const protoNum = 1
	var (
		reqType          icmp.Type = ipv4.ICMPTypeEcho
		replyType        icmp.Type = ipv4.ICMPTypeEchoReply
		timeExceededType icmp.Type = ipv4.ICMPTypeTimeExceeded
	)

	dst := &net.IPAddr{IP: addr.AsSlice()}
	icmpID := randomID16()
	seqCounter := 0
	payload := []byte("11dog-traceroute")
	buf := make([]byte, 2048)

	hops := make([]model.TracerouteHop, 0, maxHops)
	reached := false

	for ttl := 1; ttl <= maxHops; ttl++ {
		if ctx.Err() != nil {
			break
		}

		if err := conn.IPv4PacketConn().SetTTL(ttl); err != nil {
			return resolvedIP, nil, model.StatusError, err
		}

		hop := model.TracerouteHop{
			Hop:     uint32(ttl),
			Timeout: true,
		}
		var rtts []float64
		hopReached := false

		for q := 0; q < queriesPerHop; q++ {
			if ctx.Err() != nil {
				break
			}
			seqCounter++
			curSeq := seqCounter

			msg := icmp.Message{
				Type: reqType,
				Code: 0,
				Body: &icmp.Echo{
					ID:   icmpID,
					Seq:  curSeq,
					Data: payload,
				},
			}
			wire, err := msg.Marshal(nil)
			if err != nil {
				continue
			}

			sendTime := time.Now()
			if _, err := conn.WriteTo(wire, dst); err != nil {
				continue
			}

			wait := 800 * time.Millisecond
			if dl, ok := ctx.Deadline(); ok {
				if rem := time.Until(dl); rem < wait {
					wait = rem
				}
			}
			if wait <= 0 {
				wait = 50 * time.Millisecond
			}
			_ = conn.SetReadDeadline(time.Now().Add(wait))

			for {
				n, peer, rerr := conn.ReadFrom(buf)
				if rerr != nil {
					break
				}
				parsed, perr := icmp.ParseMessage(protoNum, buf[:n])
				if perr != nil {
					continue
				}

				switch parsed.Type {
				case replyType:
					echo, ok := parsed.Body.(*icmp.Echo)
					if !ok || echo.ID != icmpID || echo.Seq != curSeq {
						continue
					}
					rttMs := round2(float64(time.Since(sendTime).Microseconds()) / 1000.0)
					rtts = append(rtts, rttMs)
					if hop.IP == "" {
						hop.IP = peerIPString(peer)
					}
					hop.Timeout = false
					hopReached = true
					goto nextQuery

				case timeExceededType:
					te, ok := parsed.Body.(*icmp.TimeExceeded)
					if !ok {
						continue
					}
					origID, origSeq, matchOK := extractEmbeddedEchoIDSeq(te.Data)
					if !matchOK || origID != icmpID || origSeq != curSeq {
						continue
					}
					rttMs := round2(float64(time.Since(sendTime).Microseconds()) / 1000.0)
					rtts = append(rtts, rttMs)
					if hop.IP == "" {
						hop.IP = peerIPString(peer)
					}
					hop.Timeout = false
					goto nextQuery
				}
			}
		nextQuery:
		}

		if len(rtts) > 0 {
			_, avg, _, _ := calcRTTStats(rtts)
			hop.RTTsMs = rtts
			hop.AvgRTTMs = avg
			hop.LossPercent = round2(float64(queriesPerHop-len(rtts)) / float64(queriesPerHop) * 100.0)
		} else {
			hop.LossPercent = 100.0
		}

		if hop.IP != "" {
			ptrCtx, ptrCancel := context.WithTimeout(ctx, 250*time.Millisecond)
			names, lerr := net.DefaultResolver.LookupAddr(ptrCtx, hop.IP)
			ptrCancel()
			if lerr == nil && len(names) > 0 {
				hop.Hostname = names[0]
			}
		}

		hops = append(hops, hop)
		if onProgress != nil {
			hopCopy := hop
			onProgress(&model.ProgressPayload{TracerouteHop: &hopCopy})
		}

		if hopReached || hop.IP == resolvedIP {
			reached = true
			break
		}
	}

	status := model.StatusCompleted
	if errors.Is(ctx.Err(), context.Canceled) {
		status = model.StatusCancelled
	}
	return resolvedIP, &model.TracerouteResult{
		Reached: reached,
		Hops:    hops,
	}, status, nil
}

// extractEmbeddedEchoIDSeq extracts the original IPv4 ICMP Echo ID and Seq embedded in a TimeExceeded packet body.
func extractEmbeddedEchoIDSeq(data []byte) (id int, seq int, ok bool) {
	if len(data) < 20 {
		return 0, 0, false
	}
	ipHdrLen := int(data[0]&0x0f) * 4
	if ipHdrLen < 20 {
		return 0, 0, false
	}
	// Original ICMP Echo header needs 8 bytes: type(1), code(1), csum(2), id(2), seq(2).
	if len(data) < ipHdrLen+8 {
		return 0, 0, false
	}
	icmpHdr := data[ipHdrLen:]
	id = int(binary.BigEndian.Uint16(icmpHdr[4:6]))
	seq = int(binary.BigEndian.Uint16(icmpHdr[6:8]))
	return id, seq, true
}
