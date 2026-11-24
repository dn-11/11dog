package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type rateEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter enforces per-client-IP token bucket limits on task creation.
type IPRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateEntry
	limit   rate.Limit
	burst   int
}

// NewIPRateLimiter creates a limiter allowing rpm requests per minute with the given burst.
func NewIPRateLimiter(rpm int, burst int) *IPRateLimiter {
	if rpm <= 0 {
		rpm = 60
	}
	if burst <= 0 {
		burst = 10
	}
	return &IPRateLimiter{
		entries: make(map[string]*rateEntry),
		limit:   rate.Every(time.Minute / time.Duration(rpm)),
		burst:   burst,
	}
}

// Allow returns true if clientKey has available tokens.
func (l *IPRateLimiter) Allow(clientKey string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	entry, ok := l.entries[clientKey]
	if !ok {
		entry = &rateEntry{
			limiter: rate.NewLimiter(l.limit, l.burst),
		}
		l.entries[clientKey] = entry
	}
	entry.lastSeen = now
	return entry.limiter.Allow()
}

// StartCleanup periodically evicts idle IP entries until ctx is cancelled.
func (l *IPRateLimiter) StartCleanup(ctx context.Context) {
	if l == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-15 * time.Minute)
				l.mu.Lock()
				for k, v := range l.entries {
					if v.lastSeen.Before(cutoff) {
						delete(l.entries, k)
					}
				}
				l.mu.Unlock()
			}
		}
	}()
}

// ClientIP extracts the best-effort client IP from HTTP headers or RemoteAddr.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
