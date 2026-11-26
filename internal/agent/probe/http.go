package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/dn-11/11dog/internal/model"
)

func runHTTP(ctx context.Context, target string, p model.HTTPParams) (string, *model.HTTPResult, model.TaskStatus, error) {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = http.MethodGet
	}

	var mu sync.Mutex
	var (
		resolvedIP string
		dnsStart   time.Time
		dnsDur     time.Duration
		connStart  time.Time
		connDur    time.Duration
		tlsStart   time.Time
		tlsDur     time.Duration
		reqStart   time.Time
		readyTime  time.Time
		firstByte  time.Time
	)

	const dialNetwork = "tcp4"

	baseDialer := &net.Dialer{Timeout: 8 * time.Second}
	tr := &http.Transport{
		DisableKeepAlives: true,
		Proxy:             nil, // Probe directly inside DN11 without inheriting env proxies.
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: p.InsecureSkipVerify, //nolint:gosec
		},
		DialContext: func(dctx context.Context, _, addr string) (net.Conn, error) {
			c, err := baseDialer.DialContext(dctx, dialNetwork, addr)
			if err == nil && c.RemoteAddr() != nil {
				if h, _, splitErr := net.SplitHostPort(c.RemoteAddr().String()); splitErr == nil {
					mu.Lock()
					if resolvedIP == "" {
						resolvedIP = h
					}
					mu.Unlock()
				}
			}
			return c, err
		},
	}
	defer tr.CloseIdleConnections()

	var redirectCount uint32
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if !p.FollowRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			redirectCount++
			return nil
		},
	}

	trace := &httptrace.ClientTrace{
		DNSStart: func(_ httptrace.DNSStartInfo) {
			mu.Lock()
			if dnsStart.IsZero() {
				dnsStart = time.Now()
			}
			mu.Unlock()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			mu.Lock()
			if !dnsStart.IsZero() && dnsDur == 0 {
				dnsDur = time.Since(dnsStart)
				readyTime = time.Now()
			}
			if resolvedIP == "" && len(info.Addrs) > 0 {
				resolvedIP = info.Addrs[0].IP.String()
			}
			mu.Unlock()
		},
		ConnectStart: func(_, _ string) {
			mu.Lock()
			if connStart.IsZero() {
				connStart = time.Now()
			}
			mu.Unlock()
		},
		ConnectDone: func(_, addr string, err error) {
			mu.Lock()
			if !connStart.IsZero() && connDur == 0 {
				connDur = time.Since(connStart)
				readyTime = time.Now()
			}
			if err == nil && resolvedIP == "" {
				if h, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
					resolvedIP = h
				}
			}
			mu.Unlock()
		},
		TLSHandshakeStart: func() {
			mu.Lock()
			if tlsStart.IsZero() {
				tlsStart = time.Now()
			}
			mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			mu.Lock()
			if !tlsStart.IsZero() && tlsDur == 0 {
				tlsDur = time.Since(tlsStart)
				readyTime = time.Now()
			}
			mu.Unlock()
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			if firstByte.IsZero() {
				firstByte = time.Now()
			}
			mu.Unlock()
		},
	}

	reqCtx := httptrace.WithClientTrace(ctx, trace)
	req, err := http.NewRequestWithContext(reqCtx, method, target, nil)
	if err != nil {
		return "", nil, model.StatusError, err
	}
	req.Header.Set("User-Agent", "11Dog-Agent/1.0 (DN11 Network Probe)")
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	if p.HostHeader != "" {
		req.Host = p.HostHeader
	}

	reqStart = time.Now()
	readyTime = reqStart
	resp, err := client.Do(req)
	if err != nil {
		status := model.StatusError
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = model.StatusTimeout
		} else if errors.Is(ctx.Err(), context.Canceled) {
			status = model.StatusCancelled
		}
		mu.Lock()
		ip := resolvedIP
		mu.Unlock()
		return ip, nil, status, err
	}
	defer resp.Body.Close()

	// Cap body read to 1 MiB to protect edge node bandwidth and memory.
	nBytes, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	endTime := time.Now()
	if err != nil {
		status := model.StatusError
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = model.StatusTimeout
		}
		mu.Lock()
		ip := resolvedIP
		mu.Unlock()
		return ip, nil, status, err
	}

	mu.Lock()
	ip := resolvedIP
	if firstByte.IsZero() {
		firstByte = endTime
	}
	ttfbDur := firstByte.Sub(readyTime)
	if ttfbDur < 0 {
		ttfbDur = 0
	}
	dlDur := endTime.Sub(firstByte)
	if dlDur < 0 {
		dlDur = 0
	}
	totalDur := endTime.Sub(reqStart)
	mu.Unlock()

	var tlsVersion, tlsSubject string
	if resp.TLS != nil {
		tlsVersion = tlsVersionName(resp.TLS.Version)
		if len(resp.TLS.PeerCertificates) > 0 {
			tlsSubject = resp.TLS.PeerCertificates[0].Subject.CommonName
		}
	}

	finalURL := target
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	return ip, &model.HTTPResult{
		StatusCode:    int32(resp.StatusCode),
		StatusText:    resp.Status,
		FinalURL:      finalURL,
		ContentLength: uint64(nBytes),
		RedirectCount: redirectCount,
		TLSVersion:    tlsVersion,
		TLSSubject:    tlsSubject,
		Timing: &model.HTTPTiming{
			DNSMs:      durMs(dnsDur),
			TCPMs:      durMs(connDur),
			TLSMs:      durMs(tlsDur),
			TTFBMs:     durMs(ttfbDur),
			DownloadMs: durMs(dlDur),
			TotalMs:    durMs(totalDur),
		},
	}, model.StatusCompleted, nil
}

func durMs(d time.Duration) float64 {
	return round2(float64(d.Microseconds()) / 1000.0)
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS 1.3"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS10:
		return "TLS 1.0"
	default:
		return ""
	}
}
