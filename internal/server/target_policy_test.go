package server

import (
	"context"
	"testing"

	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/model"
	"github.com/dn-11/11dog/internal/store"
)

func TestTargetPolicyWildcardCIDRAndDBPersistence(t *testing.T) {
	st, err := store.NewSQLStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewSQLStore failed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	cfg := config.DefaultServerConfig()

	mgr, err := NewTargetPolicyManager(ctx, st, cfg)
	if err != nil {
		t.Fatalf("NewTargetPolicyManager failed: %v", err)
	}

	// Default disabled policy allows everything.
	if err := mgr.CheckTarget("https://anything.example.com/path"); err != nil {
		t.Fatalf("disabled policy should allow target, got %v", err)
	}

	// Configure allow + deny rules with domain wildcards and IPv4/IPv6 CIDRs.
	updated, err := mgr.Update(ctx, model.TargetPolicy{
		Enabled: true,
		AllowRules: []string{
			"172.16.0.0/16",
			"fd00::/48",
			"*.dn11.org",
			"example.com",
			"node-*.test.net",
		},
		DenyRules: []string{
			"172.16.1.0/24",
			"secret.dn11.org",
			"*.blocked.dn11.org",
		},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !updated.Enabled || updated.UpdatedAtMs == 0 {
		t.Fatalf("unexpected updated policy: %+v", updated)
	}

	allowedCases := []string{
		"172.16.2.10",
		"http://172.16.2.10:8080/healthz",
		"fd00::1234",
		"[fd00::1234]:443",
		"a.dn11.org",
		"a.b.dn11.org",
		"https://sub.dn11.org:8443/api",
		"EXAMPLE.COM.",
		"node-hk01.test.net",
	}
	for _, target := range allowedCases {
		if err := mgr.CheckTarget(target); err != nil {
			t.Errorf("expected target %q to be allowed, got error: %v", target, err)
		}
	}

	deniedCases := []string{
		"172.16.1.55",                 // hits deny CIDR overriding allow CIDR
		"8.8.8.8",                     // outside allow CIDRs
		"2001:db8::1",                 // outside allow CIDRs
		"secret.dn11.org",             // hits exact deny domain overriding *.dn11.org
		"x.blocked.dn11.org",          // hits wildcard deny domain
		"dn11.org",                    // apex does not match *.dn11.org
		"sub.example.com",             // does not match exact example.com
		"https://evil.com/test",       // outside allow domains
	}
	for _, target := range deniedCases {
		if err := mgr.CheckTarget(target); err == nil {
			t.Errorf("expected target %q to be rejected, but it was allowed", target)
		}
	}

	// Verify DB persistence: reloading a fresh manager from the same DB restores rules into memory.
	mgr2, err := NewTargetPolicyManager(ctx, st, cfg)
	if err != nil {
		t.Fatalf("reload TargetPolicyManager failed: %v", err)
	}
	if !mgr2.Get().Enabled || len(mgr2.Get().AllowRules) != 5 || len(mgr2.Get().DenyRules) != 3 {
		t.Fatalf("reloaded policy mismatch: %+v", mgr2.Get())
	}
	if err := mgr2.CheckTarget("secret.dn11.org"); err == nil {
		t.Fatal("reloaded manager should reject secret.dn11.org")
	}
	if err := mgr2.CheckTarget("ok.dn11.org"); err != nil {
		t.Fatalf("reloaded manager should allow ok.dn11.org: %v", err)
	}

	// Invalid CIDR / domain rules are rejected without corrupting in-memory cache.
	if _, err := mgr2.Update(ctx, model.TargetPolicy{
		Enabled:    true,
		AllowRules: []string{"172.16.0.0/99"},
	}); err == nil {
		t.Fatal("expected error for invalid CIDR rule")
	}
	if _, err := mgr2.Update(ctx, model.TargetPolicy{
		Enabled:   true,
		DenyRules: []string{"bad domain!@#"},
	}); err == nil {
		t.Fatal("expected error for invalid domain rule")
	}

	// Empty allow/deny rule lists are valid and must return non-nil empty slices.
	emptyPol, err := mgr2.Update(ctx, model.TargetPolicy{
		Enabled:    true,
		AllowRules: nil,
		DenyRules:  []string{},
	})
	if err != nil {
		t.Fatalf("Update with empty rules failed: %v", err)
	}
	if emptyPol.AllowRules == nil || emptyPol.DenyRules == nil || len(emptyPol.AllowRules) != 0 || len(emptyPol.DenyRules) != 0 {
		t.Fatalf("expected non-nil empty slices, got %#v", emptyPol)
	}
}
