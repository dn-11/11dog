package store

import (
	"context"
	"errors"
	"testing"
)

func TestSQLiteNodeStoreCRUDAndTokenRotation(t *testing.T) {
	st, err := NewSQLStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewSQLStore failed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	created, err := st.CreateNode(ctx, &NodeRecord{
		ID:             "node-hk-01",
		Name:           "香港 BGP 01",
		Region:         "香港",
		Group:          "DN11-BGP",
		ASN:            4211110101,
		MaxConcurrency: 12,
		Enabled:        true,
		Notes:          "initial note",
	})
	if err != nil {
		t.Fatalf("CreateNode failed: %v", err)
	}
	if created.Token == "" {
		t.Fatal("expected non-empty generated token")
	}

	// Lookup by token
	byToken, err := st.GetNodeByToken(ctx, created.Token)
	if err != nil {
		t.Fatalf("GetNodeByToken failed: %v", err)
	}
	if byToken.ID != "node-hk-01" || byToken.Name != "香港 BGP 01" {
		t.Fatalf("unexpected GetNodeByToken record: %+v", byToken)
	}

	// Duplicate ID fails with ErrConflict
	if _, err := st.CreateNode(ctx, &NodeRecord{ID: "node-hk-01", Name: "dup", Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on duplicate ID, got %v", err)
	}

	// Update metadata
	newName := "香港 BGP Pro"
	newRegion := "亚太"
	disabled := false
	updated, err := st.UpdateNode(ctx, created.ID, NodePatch{
		Name:    &newName,
		Region:  &newRegion,
		Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("UpdateNode failed: %v", err)
	}
	if updated.Name != newName || updated.Region != newRegion || updated.Enabled {
		t.Fatalf("unexpected updated node: %+v", updated)
	}

	// Rotate token invalidates old token
	rotated, err := st.RotateToken(ctx, created.ID)
	if err != nil {
		t.Fatalf("RotateToken failed: %v", err)
	}
	if rotated.Token == created.Token {
		t.Fatal("expected rotated token to differ from original")
	}
	if _, err := st.GetNodeByToken(ctx, created.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old token to return ErrNotFound, got %v", err)
	}
	if _, err := st.GetNodeByToken(ctx, rotated.Token); err != nil {
		t.Fatalf("expected new token lookup to succeed, got %v", err)
	}

	// List & Delete
	list, err := st.ListNodes(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListNodes = %+v, err=%v; want 1 node", list, err)
	}
	if err := st.DeleteNode(ctx, created.ID); err != nil {
		t.Fatalf("DeleteNode failed: %v", err)
	}
	if _, err := st.GetNode(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
