package natsauth

import (
	"strings"
	"testing"

	"github.com/nats-io/nkeys"
)

func TestManagerIssueCreds(t *testing.T) {
	if _, err := NewManager("", "DOG_TASKS"); err == nil {
		t.Fatal("expected error when account seed is empty")
	}

	accKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	accSeed, err := accKP.Seed()
	if err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(string(accSeed), "DOG_TASKS")
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if !strings.HasPrefix(mgr.AccountPub(), "A") {
		t.Fatalf("unexpected account pub: %s", mgr.AccountPub())
	}

	creds, err := mgr.IssueAgentCreds("node-hk-01")
	if err != nil {
		t.Fatalf("IssueAgentCreds failed: %v", err)
	}
	nodeID, err := ExtractNodeIDFromCreds([]byte(creds))
	if err != nil || nodeID != "node-hk-01" {
		t.Fatalf("ExtractNodeIDFromCreds = (%q, %v), want (node-hk-01, nil)", nodeID, err)
	}
}
