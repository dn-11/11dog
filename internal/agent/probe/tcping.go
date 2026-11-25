package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dn-11/11dog/internal/model"
)

func runTcping(ctx context.Context, target string, p model.TcpingParams) (string, *model.TcpingResult, model.TaskStatus, error) {
	if p.Port == 0 || p.Port > 65535 {
		return "", nil, model.StatusError, fmt.Errorf("invalid port %d", p.Port)
	}
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

	network := "tcp4"
	if addr.Is6() {
		network = "tcp6"
	}
	dialAddr := net.JoinHostPort(resolvedIP, strconv.Itoa(int(p.Port)))

	var rtts []float64
	var connected uint32
	var sent uint32
	refusedCount := 0

	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		sent++

		attemptTimeout := 2500 * time.Millisecond
		if dl, ok := ctx.Deadline(); ok {
			if rem := time.Until(dl); rem < attemptTimeout {
				attemptTimeout = rem
			}
		}
		if attemptTimeout <= 0 {
			attemptTimeout = 50 * time.Millisecond
		}

		dialer := net.Dialer{Timeout: attemptTimeout}
		start := time.Now()
		conn, err := dialer.DialContext(ctx, network, dialAddr)
		if err == nil {
			rttMs := round2(float64(time.Since(start).Microseconds()) / 1000.0)
			_ = conn.Close()
			connected++
			rtts = append(rtts, rttMs)
		} else if isConnRefused(err) {
			refusedCount++
		}

		if i < count-1 {
			select {
			case <-ctx.Done():
			case <-time.After(interval):
			}
		}
	}

	loss := 100.0
	if sent > 0 {
		loss = round2((float64(sent-connected) / float64(sent)) * 100.0)
	}
	minRTT, avgRTT, maxRTT, _ := calcRTTStats(rtts)

	state := "timeout"
	status := model.StatusTimeout
	if connected > 0 {
		state = "open"
		status = model.StatusCompleted
	} else if refusedCount > 0 {
		state = "refused"
		status = model.StatusCompleted
	} else if errors.Is(ctx.Err(), context.Canceled) {
		status = model.StatusCancelled
	}

	return resolvedIP, &model.TcpingResult{
		Port:        p.Port,
		Sent:        sent,
		Connected:   connected,
		LossPercent: loss,
		MinRTTMs:    minRTT,
		AvgRTTMs:    avgRTT,
		MaxRTTMs:    maxRTT,
		State:       state,
		RTTsMs:      rtts,
	}, status, nil
}

func isConnRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused")
}
