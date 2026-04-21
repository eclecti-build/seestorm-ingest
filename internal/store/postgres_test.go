package store

import (
	"testing"
	"time"
)

// TestBuildPoolConfig_AppliesAuditConstants locks in the pgx pool shape
// settled in the swarm audit (see docs/SWARM_AUDIT_2026-04-18.md
// "Constants — paste-ready" → pgx pool config). Pure unit test — no DB
// connection required since pgxpool.ParseConfig only validates the URL
// shape, it does not dial.
func TestBuildPoolConfig_AppliesAuditConstants(t *testing.T) {
	t.Parallel()

	// Valid DSN shape; no network interaction happens until pool.Ping.
	url := "postgres://user:pass@localhost:5432/seestorm?sslmode=disable"
	cfg, err := buildPoolConfig(url)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}

	if got, want := cfg.MaxConns, int32(16); got != want {
		t.Errorf("MaxConns: got %d want %d", got, want)
	}
	if got, want := cfg.MinConns, int32(2); got != want {
		t.Errorf("MinConns: got %d want %d", got, want)
	}
	if got, want := cfg.MaxConnIdleTime, 4*time.Minute; got != want {
		t.Errorf("MaxConnIdleTime: got %v want %v", got, want)
	}
	if got, want := cfg.MaxConnLifetime, 30*time.Minute; got != want {
		t.Errorf("MaxConnLifetime: got %v want %v", got, want)
	}
	if got, want := cfg.ConnConfig.RuntimeParams["statement_timeout"], "15000"; got != want {
		t.Errorf("statement_timeout: got %q want %q", got, want)
	}
}

// TestBuildPoolConfig_RejectsInvalidURL — edge case. A malformed DSN should
// surface as a wrapped error, not a nil-config-return that panics later.
func TestBuildPoolConfig_RejectsInvalidURL(t *testing.T) {
	t.Parallel()

	_, err := buildPoolConfig("not-a-valid-dsn://::broken")
	if err == nil {
		t.Fatal("expected error for malformed DSN, got nil")
	}
}
