package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/store"
	natsgo "github.com/nats-io/nats.go"
)

const (
	targetPolicySettingKey = "target_policy"
	targetPolicySyncSubj   = "sys.config.target_policy"
)

var domainRuleCharRe = regexp.MustCompile(`^[a-z0-9.*-]+$`)

type domainRule struct {
	raw     string
	pattern *regexp.Regexp
}

type compiledTargetRuleSet struct {
	cidrs   []netip.Prefix
	domains []domainRule
}

type compiledTargetPolicy struct {
	enabled bool
	allow   compiledTargetRuleSet
	deny    compiledTargetRuleSet
}

// TargetPolicyManager manages global probe target restrictions (domain wildcards & CIDRs),
// persisted in the SQL store and cached in memory for zero-DB-overhead validation.
type TargetPolicyManager struct {
	store store.NodeStore
	nc    *natsgo.Conn

	mu       sync.RWMutex
	policy   model.TargetPolicy
	compiled compiledTargetPolicy
	syncSub  *natsgo.Subscription
}

// AttachNATS enables cross-replica in-memory cache invalidation/sync over Core NATS.
func (m *TargetPolicyManager) AttachNATS(nc *natsgo.Conn) {
	if nc == nil {
		return
	}
	m.nc = nc
	sub, err := nc.Subscribe(targetPolicySyncSubj, func(msg *natsgo.Msg) {
		var p model.TargetPolicy
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return
		}
		norm, compiled, err := normalizeAndCompilePolicy(p)
		if err != nil {
			return
		}
		m.mu.Lock()
		m.policy = norm
		m.compiled = compiled
		m.mu.Unlock()
	})
	if err == nil {
		m.mu.Lock()
		m.syncSub = sub
		m.mu.Unlock()
	}
}

// NewTargetPolicyManager loads the target policy from DB into memory, falling back
// to static YAML allowed_cidrs / denied_cidrs if no DB record exists yet.
func NewTargetPolicyManager(ctx context.Context, st store.NodeStore, cfg *config.ServerConfig) (*TargetPolicyManager, error) {
	mgr := &TargetPolicyManager{store: st}

	var initial model.TargetPolicy
	loadedFromDB := false
	if st != nil {
		raw, updatedAt, err := st.GetSetting(ctx, targetPolicySettingKey)
		if err == nil {
			if err := json.Unmarshal([]byte(raw), &initial); err != nil {
				return nil, fmt.Errorf("decode stored target_policy: %w", err)
			}
			initial.UpdatedAtMs = updatedAt
			loadedFromDB = true
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("load target_policy from store: %w", err)
		}
	}

	if !loadedFromDB && cfg != nil {
		initial.AllowRules = append([]string(nil), cfg.Limits.AllowedCIDRs...)
		initial.DenyRules = append([]string(nil), cfg.Limits.DeniedCIDRs...)
		initial.Enabled = len(initial.AllowRules) > 0 || len(initial.DenyRules) > 0
	}

	norm, compiled, err := normalizeAndCompilePolicy(initial)
	if err != nil {
		return nil, err
	}
	mgr.policy = norm
	mgr.compiled = compiled
	return mgr, nil
}

// Get returns a snapshot of the current in-memory cached target policy.
func (m *TargetPolicyManager) Get() model.TargetPolicy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return model.TargetPolicy{
		Enabled:     m.policy.Enabled,
		AllowRules:  append([]string(nil), m.policy.AllowRules...),
		DenyRules:   append([]string(nil), m.policy.DenyRules...),
		UpdatedAtMs: m.policy.UpdatedAtMs,
	}
}

// Update validates, compiles, persists to DB, and atomically refreshes the in-memory cache.
func (m *TargetPolicyManager) Update(ctx context.Context, p model.TargetPolicy) (model.TargetPolicy, error) {
	norm, compiled, err := normalizeAndCompilePolicy(p)
	if err != nil {
		return model.TargetPolicy{}, err
	}

	if m.store != nil {
		payload, err := json.Marshal(struct {
			Enabled    bool     `json:"enabled"`
			AllowRules []string `json:"allow_rules"`
			DenyRules  []string `json:"deny_rules"`
		}{
			Enabled:    norm.Enabled,
			AllowRules: norm.AllowRules,
			DenyRules:  norm.DenyRules,
		})
		if err != nil {
			return model.TargetPolicy{}, fmt.Errorf("encode target_policy: %w", err)
		}
		updatedAt, err := m.store.SetSetting(ctx, targetPolicySettingKey, string(payload))
		if err != nil {
			return model.TargetPolicy{}, fmt.Errorf("save target_policy: %w", err)
		}
		norm.UpdatedAtMs = updatedAt
	}

	m.mu.Lock()
	m.policy = norm
	m.compiled = compiled
	m.mu.Unlock()

	if m.nc != nil {
		if syncBytes, err := json.Marshal(norm); err == nil {
			_ = m.nc.Publish(targetPolicySyncSubj, syncBytes)
		}
	}

	return m.Get(), nil
}

// CheckTarget evaluates a probe target (IP, hostname, or URL) against the in-memory cached policy.
func (m *TargetPolicyManager) CheckTarget(rawTarget string) error {
	m.mu.RLock()
	c := m.compiled
	m.mu.RUnlock()

	if !c.enabled {
		return nil
	}

	host := extractTargetHost(rawTarget)
	if host == "" {
		return fmt.Errorf("%w: unable to extract host from target %q", ErrInvalidRequest, rawTarget)
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if matched, rule := c.deny.matchIP(addr); matched {
			return fmt.Errorf("%w: 目标 IP %s 命中全局禁止规则 %s", ErrInvalidRequest, addr, rule)
		}
		if c.allow.hasAny() {
			if matched, _ := c.allow.matchIP(addr); !matched {
				return fmt.Errorf("%w: 目标 IP %s 不在全局允许的测试范围内", ErrInvalidRequest, addr)
			}
		}
		return nil
	}

	domain := strings.TrimSuffix(strings.ToLower(host), ".")
	if matched, rule := c.deny.matchDomain(domain); matched {
		return fmt.Errorf("%w: 目标域名 %s 命中全局禁止规则 %s", ErrInvalidRequest, domain, rule)
	}
	if c.allow.hasAny() {
		if matched, _ := c.allow.matchDomain(domain); !matched {
			return fmt.Errorf("%w: 目标域名 %s 不在全局允许的测试范围内", ErrInvalidRequest, domain)
		}
	}
	return nil
}

func extractTargetHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return strings.Trim(u.Hostname(), "[]")
		}
	}
	trimmed := strings.Trim(s, "[]")
	if _, err := netip.ParseAddr(trimmed); err == nil {
		return trimmed
	}
	if host, _, err := net.SplitHostPort(s); err == nil && host != "" {
		return strings.Trim(host, "[]")
	}
	if idx := strings.Index(s, "/"); idx > 0 {
		s = s[:idx]
	}
	return strings.Trim(s, "[]")
}

func (rs *compiledTargetRuleSet) hasAny() bool {
	return len(rs.cidrs) > 0 || len(rs.domains) > 0
}

func (rs *compiledTargetRuleSet) matchIP(addr netip.Addr) (bool, string) {
	for _, p := range rs.cidrs {
		if p.Contains(addr) {
			return true, p.String()
		}
	}
	return false, ""
}

func (rs *compiledTargetRuleSet) matchDomain(domain string) (bool, string) {
	for _, dr := range rs.domains {
		if dr.pattern.MatchString(domain) {
			return true, dr.raw
		}
	}
	return false, ""
}

func normalizeAndCompilePolicy(in model.TargetPolicy) (model.TargetPolicy, compiledTargetPolicy, error) {
	allowRules, allowCompiled, err := compileRuleList("allow_rules", in.AllowRules)
	if err != nil {
		return model.TargetPolicy{}, compiledTargetPolicy{}, err
	}
	denyRules, denyCompiled, err := compileRuleList("deny_rules", in.DenyRules)
	if err != nil {
		return model.TargetPolicy{}, compiledTargetPolicy{}, err
	}
	norm := model.TargetPolicy{
		Enabled:     in.Enabled,
		AllowRules:  allowRules,
		DenyRules:   denyRules,
		UpdatedAtMs: in.UpdatedAtMs,
	}
	comp := compiledTargetPolicy{
		enabled: in.Enabled,
		allow:   allowCompiled,
		deny:    denyCompiled,
	}
	return norm, comp, nil
}

func compileRuleList(field string, rules []string) ([]string, compiledTargetRuleSet, error) {
	cleaned := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	var rs compiledTargetRuleSet

	for _, raw := range rules {
		r := strings.TrimSpace(raw)
		if r == "" || strings.HasPrefix(r, "#") {
			continue
		}
		if strings.Contains(r, "/") {
			p, err := netip.ParsePrefix(r)
			if err != nil {
				return nil, compiledTargetRuleSet{}, fmt.Errorf("%s 包含无效 CIDR 规则 %q: %w", field, r, err)
			}
			p = p.Masked()
			canon := p.String()
			if _, dup := seen[canon]; !dup {
				seen[canon] = struct{}{}
				cleaned = append(cleaned, canon)
				rs.cidrs = append(rs.cidrs, p)
			}
			continue
		}

		unbracketed := strings.Trim(r, "[]")
		if addr, err := netip.ParseAddr(unbracketed); err == nil {
			addr = addr.Unmap()
			p := netip.PrefixFrom(addr, addr.BitLen())
			canon := addr.String()
			if _, dup := seen[canon]; !dup {
				seen[canon] = struct{}{}
				cleaned = append(cleaned, canon)
				rs.cidrs = append(rs.cidrs, p)
			}
			continue
		}

		dom := strings.TrimSuffix(strings.ToLower(r), ".")
		if dom == "" || dom == "." || strings.Contains(dom, "..") || !domainRuleCharRe.MatchString(dom) {
			return nil, compiledTargetRuleSet{}, fmt.Errorf("%s 包含无效域名通配符规则 %q", field, r)
		}
		re, err := compileDomainWildcard(dom)
		if err != nil {
			return nil, compiledTargetRuleSet{}, fmt.Errorf("%s 包含无效域名通配符规则 %q: %w", field, r, err)
		}
		if _, dup := seen[dom]; !dup {
			seen[dom] = struct{}{}
			cleaned = append(cleaned, dom)
			rs.domains = append(rs.domains, domainRule{raw: dom, pattern: re})
		}
	}

	return cleaned, rs, nil
}

func compileDomainWildcard(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	if strings.HasPrefix(pattern, "*.") {
		// Standard subdomain wildcard: "*.example.com" matches "a.example.com" and "a.b.example.com", not "example.com".
		b.WriteString(`.+\.`)
		pattern = pattern[2:]
	}
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			b.WriteString(".*")
		case '.':
			b.WriteString(`\.`)
		default:
			b.WriteByte(ch)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
