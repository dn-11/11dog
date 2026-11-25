package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// SQLStore implements NodeStore using SQLite (pure Go) or PostgreSQL.
type SQLStore struct {
	db     *sql.DB
	driver string
}

// NewSQLStore opens the configured database driver ("sqlite" or "postgres") and runs migrations.
func NewSQLStore(driver, dsn string) (*SQLStore, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		driver = "sqlite"
	}

	var sqlDriver string
	switch driver {
	case "sqlite":
		sqlDriver = "sqlite"
		if dsn == "" {
			dsn = "./data/11dog.db"
		}
		if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
			if dir := filepath.Dir(dsn); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, fmt.Errorf("create sqlite data dir %q: %w", dir, err)
				}
			}
		}
	case "postgres", "postgresql":
		driver = "postgres"
		sqlDriver = "pgx"
		if dsn == "" {
			return nil, errors.New("postgres dsn is required")
		}
	default:
		return nil, fmt.Errorf("unsupported db driver %q (expected sqlite or postgres)", driver)
	}

	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s db: %w", driver, err)
	}

	if driver == "sqlite" {
		// SQLite supports one writer at a time cleanly; serializing avoids SQLITE_BUSY under bursts.
		db.SetMaxOpenConns(1)
		if dsn != ":memory:" && !strings.HasPrefix(dsn, "file::memory:") {
			if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("configure sqlite pragmas: %w", err)
			}
		}
	} else {
		db.SetMaxOpenConns(15)
		db.SetMaxIdleConns(5)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s db: %w", driver, err)
	}

	s := &SQLStore{db: db, driver: driver}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s schema: %w", driver, err)
	}
	return s, nil
}

func (s *SQLStore) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			region TEXT NOT NULL,
			node_group TEXT NOT NULL,
			asn BIGINT NOT NULL DEFAULT 0,
			max_concurrency INTEGER NOT NULL DEFAULT 8,
			token TEXT NOT NULL UNIQUE,
			creds TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			notes TEXT NOT NULL DEFAULT '',
			created_at_ms BIGINT NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_nodes_token ON nodes(token)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	// Backward-compatible migration for existing databases lacking the creds column.
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE nodes ADD COLUMN creds TEXT NOT NULL DEFAULT ''`); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "already exists") {
			return err
		}
	}
	return nil
}

func (s *SQLStore) rebind(query string) string {
	if s.driver != "postgres" {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	idx := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(idx))
			idx++
		} else {
			b.WriteByte(query[i])
		}
	}
	return b.String()
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate") || strings.Contains(msg, "23505")
}

// CreateNode persists a new authoritative node record and generates ID/Token if empty.
func (s *SQLStore) CreateNode(ctx context.Context, rec *NodeRecord) (*NodeRecord, error) {
	now := time.Now().UnixMilli()
	cp := *rec
	if strings.TrimSpace(cp.ID) == "" {
		cp.ID = GenerateNodeID()
	}
	if strings.TrimSpace(cp.Token) == "" {
		cp.Token = GenerateNodeToken()
	}
	if cp.MaxConcurrency == 0 {
		cp.MaxConcurrency = 8
	}
	if cp.Region == "" {
		cp.Region = "DN11"
	}
	if cp.Group == "" {
		cp.Group = "Default"
	}
	if cp.Name == "" {
		cp.Name = cp.ID
	}
	cp.CreatedAtMs = now
	cp.UpdatedAtMs = now

	q := s.rebind(`INSERT INTO nodes (
		id, name, region, node_group, asn, max_concurrency, token, creds, enabled, notes, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	_, err := s.db.ExecContext(
		ctx, q,
		cp.ID, cp.Name, cp.Region, cp.Group, int64(cp.ASN), int(cp.MaxConcurrency),
		cp.Token, cp.Creds, cp.Enabled, cp.Notes, cp.CreatedAtMs, cp.UpdatedAtMs,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return &cp, nil
}

func scanNode(scanner interface{ Scan(dest ...any) error }) (*NodeRecord, error) {
	var rec NodeRecord
	var asn int64
	var maxConc int
	err := scanner.Scan(
		&rec.ID, &rec.Name, &rec.Region, &rec.Group, &asn, &maxConc,
		&rec.Token, &rec.Creds, &rec.Enabled, &rec.Notes, &rec.CreatedAtMs, &rec.UpdatedAtMs,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rec.ASN = uint32(asn)
	rec.MaxConcurrency = uint32(maxConc)
	return &rec, nil
}

const selectNodeCols = `id, name, region, node_group, asn, max_concurrency, token, creds, enabled, notes, created_at_ms, updated_at_ms`

// GetNode fetches a node record by ID.
func (s *SQLStore) GetNode(ctx context.Context, id string) (*NodeRecord, error) {
	q := s.rebind(`SELECT ` + selectNodeCols + ` FROM nodes WHERE id = ?`)
	return scanNode(s.db.QueryRowContext(ctx, q, id))
}

// GetNodeByToken fetches a node record by its unique communication token.
func (s *SQLStore) GetNodeByToken(ctx context.Context, token string) (*NodeRecord, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNotFound
	}
	q := s.rebind(`SELECT ` + selectNodeCols + ` FROM nodes WHERE token = ?`)
	return scanNode(s.db.QueryRowContext(ctx, q, token))
}

// ListNodes returns all stored node records ordered by region, group, and name.
func (s *SQLStore) ListNodes(ctx context.Context) ([]*NodeRecord, error) {
	q := `SELECT ` + selectNodeCols + ` FROM nodes ORDER BY region ASC, node_group ASC, name ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*NodeRecord
	for rows.Next() {
		rec, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateNode applies mutable fields to an existing node record and returns the updated record.
func (s *SQLStore) UpdateNode(ctx context.Context, id string, patch NodePatch) (*NodeRecord, error) {
	cur, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		if v := strings.TrimSpace(*patch.Name); v != "" {
			cur.Name = v
		}
	}
	if patch.Region != nil {
		if v := strings.TrimSpace(*patch.Region); v != "" {
			cur.Region = v
		}
	}
	if patch.Group != nil {
		if v := strings.TrimSpace(*patch.Group); v != "" {
			cur.Group = v
		}
	}
	if patch.ASN != nil {
		cur.ASN = *patch.ASN
	}
	if patch.MaxConcurrency != nil && *patch.MaxConcurrency > 0 {
		cur.MaxConcurrency = *patch.MaxConcurrency
	}
	if patch.Enabled != nil {
		cur.Enabled = *patch.Enabled
	}
	if patch.Notes != nil {
		cur.Notes = *patch.Notes
	}
	cur.UpdatedAtMs = time.Now().UnixMilli()

	q := s.rebind(`UPDATE nodes
		SET name = ?, region = ?, node_group = ?, asn = ?, max_concurrency = ?, enabled = ?, notes = ?, updated_at_ms = ?
		WHERE id = ?`)
	res, err := s.db.ExecContext(
		ctx, q,
		cur.Name, cur.Region, cur.Group, int64(cur.ASN), int(cur.MaxConcurrency),
		cur.Enabled, cur.Notes, cur.UpdatedAtMs, cur.ID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return cur, nil
}

// RotateToken replaces a node's install-script token with a fresh random token.
func (s *SQLStore) RotateToken(ctx context.Context, id string) (*NodeRecord, error) {
	cur, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	cur.Token = GenerateNodeToken()
	cur.UpdatedAtMs = time.Now().UnixMilli()

	q := s.rebind(`UPDATE nodes SET token = ?, updated_at_ms = ? WHERE id = ?`)
	res, err := s.db.ExecContext(ctx, q, cur.Token, cur.UpdatedAtMs, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return cur, nil
}

// UpdateCreds stores a newly signed NATS .creds bundle and rotates the install token.
func (s *SQLStore) UpdateCreds(ctx context.Context, id, creds string) (*NodeRecord, error) {
	cur, err := s.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	cur.Creds = creds
	cur.Token = GenerateNodeToken()
	cur.UpdatedAtMs = time.Now().UnixMilli()

	q := s.rebind(`UPDATE nodes SET creds = ?, token = ?, updated_at_ms = ? WHERE id = ?`)
	res, err := s.db.ExecContext(ctx, q, cur.Creds, cur.Token, cur.UpdatedAtMs, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return cur, nil
}

// DeleteNode deletes a node record by ID.
func (s *SQLStore) DeleteNode(ctx context.Context, id string) error {
	q := s.rebind(`DELETE FROM nodes WHERE id = ?`)
	res, err := s.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetSetting retrieves a persisted global setting value and its update timestamp.
func (s *SQLStore) GetSetting(ctx context.Context, key string) (string, int64, error) {
	q := s.rebind(`SELECT value, updated_at_ms FROM settings WHERE key = ?`)
	var value string
	var updatedAtMs int64
	if err := s.db.QueryRowContext(ctx, q, key).Scan(&value, &updatedAtMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, ErrNotFound
		}
		return "", 0, err
	}
	return value, updatedAtMs, nil
}

// SetSetting upserts a persisted global setting value and returns the update timestamp.
func (s *SQLStore) SetSetting(ctx context.Context, key, value string) (int64, error) {
	now := time.Now().UnixMilli()
	q := s.rebind(`INSERT INTO settings (key, value, updated_at_ms)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at_ms = excluded.updated_at_ms`)
	if _, err := s.db.ExecContext(ctx, q, key, value, now); err != nil {
		return 0, err
	}
	return now, nil
}

// Close closes the underlying database pool.
func (s *SQLStore) Close() error {
	return s.db.Close()
}
