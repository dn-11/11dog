package natsauth

import (
	"strings"
	"testing"

	"github.com/google/uuid"
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

	wantID := uuid.New()
	creds, err := mgr.IssueAgentCreds(wantID)
	if err != nil {
		t.Fatalf("IssueAgentCreds failed: %v", err)
	}
	nodeID, err := ExtractNodeIDFromCreds([]byte(creds))
	if err != nil || nodeID != wantID {
		t.Fatalf("ExtractNodeIDFromCreds = (%v, %v), want (%v, nil)", nodeID, err, wantID)
	}
}
