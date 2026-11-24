package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

var (
	// ErrNotFound is returned when a requested node record does not exist.
	ErrNotFound = errors.New("node not found")
	// ErrConflict is returned when a node ID or communication token already exists.
	ErrConflict = errors.New("node already exists")
)

// NodeRecord represents the authoritative server-side identity and settings of a probe node.
type NodeRecord struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Region         string `json:"region"`
	Group          string `json:"group"`
	ASN            uint32 `json:"asn"`
	MaxConcurrency uint32 `json:"max_concurrency"`
	Token          string `json:"token"`
	Enabled        bool   `json:"enabled"`
	Notes          string `json:"notes"`
	CreatedAtMs    int64  `json:"created_at_ms"`
	UpdatedAtMs    int64  `json:"updated_at_ms"`
}

// NodePatch holds mutable fields for updating an existing node record.
type NodePatch struct {
	Name           *string
	Region         *string
	Group          *string
	ASN            *uint32
	MaxConcurrency *uint32
	Enabled        *bool
	Notes          *string
}

// NodeStore persists authoritative node identities and per-node authentication tokens.
type NodeStore interface {
	CreateNode(ctx context.Context, rec *NodeRecord) (*NodeRecord, error)
	GetNode(ctx context.Context, id string) (*NodeRecord, error)
	GetNodeByToken(ctx context.Context, token string) (*NodeRecord, error)
	ListNodes(ctx context.Context) ([]*NodeRecord, error)
	UpdateNode(ctx context.Context, id string, patch NodePatch) (*NodeRecord, error)
	RotateToken(ctx context.Context, id string) (*NodeRecord, error)
	DeleteNode(ctx context.Context, id string) error
	Close() error
}

// GenerateNodeID generates a short, unique node identifier.
func GenerateNodeID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "node-" + hex.EncodeToString(b[:])
}

// GenerateNodeToken generates a cryptographically random per-node communication token.
func GenerateNodeToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "dog_" + hex.EncodeToString(b[:])
}
