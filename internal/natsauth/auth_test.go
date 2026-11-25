package natsauth

import (
	"strings"
	"testing"
)

func TestManagerIssueCredsAndRenderConfig(t *testing.T) {
	mgr, err := NewManager("", "", "DOG_TASKS")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if !strings.HasPrefix(mgr.OperatorPub(), "O") || !strings.HasPrefix(mgr.AccountPub(), "A") {
		t.Fatalf("unexpected keys: op=%s acc=%s", mgr.OperatorPub(), mgr.AccountPub())
	}

	// Reload with same seeds produces identical public keys.
	mgr2, err := NewManager(mgr.OperatorSeed(), mgr.AccountSeed(), "DOG_TASKS")
	if err != nil {
		t.Fatalf("reload NewManager failed: %v", err)
	}
	if mgr2.OperatorPub() != mgr.OperatorPub() || mgr2.AccountPub() != mgr.AccountPub() {
		t.Fatal("reloaded keys do not match")
	}

	creds, err := mgr.IssueAgentCreds("node-hk-01")
	if err != nil {
		t.Fatalf("IssueAgentCreds failed: %v", err)
	}
	nodeID, err := ExtractNodeIDFromCreds([]byte(creds))
	if err != nil || nodeID != "node-hk-01" {
		t.Fatalf("ExtractNodeIDFromCreds = (%q, %v), want (node-hk-01, nil)", nodeID, err)
	}

	conf := mgr.RenderNATSServerConfig("/data/js")
	if !strings.Contains(conf, "operator: "+mgr.OperatorJWT()) ||
		!strings.Contains(conf, mgr.AccountPub()+": "+mgr.AccountJWT()) {
		t.Fatalf("unexpected nats-server.conf:\n%s", conf)
	}
}
