package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
)

var (
	// ErrNotFound is returned when a requested node record does not exist.
	ErrNotFound = errors.New("node not found")
	// ErrConflict is returned when a node ID or communication token already exists.
	ErrConflict = errors.New("node already exists")
)

// NodeRecord represents the authoritative server-side identity and NATS credentials of a probe node.
type NodeRecord struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	Region         string    `json:"region"`
	Group          string    `json:"group"`
	ASN            uint32    `json:"asn"`
	MaxConcurrency uint32    `json:"max_concurrency"`
	Token          string    `json:"token"`
	Creds          string    `json:"creds"`
	Enabled        bool      `json:"enabled"`
	Notes          string    `json:"notes"`
	CreatedAtMs    int64     `json:"created_at_ms"`
	UpdatedAtMs    int64     `json:"updated_at_ms"`
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

// NodeStore persists authoritative node identities, per-node NATS credentials, and global settings.
type NodeStore interface {
	CreateNode(ctx context.Context, rec *NodeRecord) (*NodeRecord, error)
	GetNode(ctx context.Context, id uuid.UUID) (*NodeRecord, error)
	GetNodeByToken(ctx context.Context, token string) (*NodeRecord, error)
	ListNodes(ctx context.Context) ([]*NodeRecord, error)
	UpdateNode(ctx context.Context, id uuid.UUID, patch NodePatch) (*NodeRecord, error)
	RotateToken(ctx context.Context, id uuid.UUID) (*NodeRecord, error)
	UpdateCreds(ctx context.Context, id uuid.UUID, creds string) (*NodeRecord, error)
	DeleteNode(ctx context.Context, id uuid.UUID) error
	GetSetting(ctx context.Context, key string) (string, int64, error)
	SetSetting(ctx context.Context, key, value string) (int64, error)
	Close() error
}

// GenerateNodeID generates a new random UUIDv4 node identifier.
func GenerateNodeID() uuid.UUID {
	return uuid.New()
}

// GenerateNodeToken generates a cryptographically random install-script token.
func GenerateNodeToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "dog_" + hex.EncodeToString(b[:])
}
