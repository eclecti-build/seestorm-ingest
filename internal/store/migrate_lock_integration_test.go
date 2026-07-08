package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestMigrate_SerializesOnAdvisoryLock proves Migrate blocks while another
// session holds the same advisory lock key, and proceeds once that
// session releases it. Uses a raw pgx connection (not a Store) to hold
// the lock manually so the test controls exactly when it releases.
func TestMigrate_SerializesOnAdvisoryLock(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()
	// Establish the schema once up front so this test isolates lock
	// contention, not first-boot DDL races (that's covered by
	// TestMigrate_ConcurrentBootsAllSucceed below).
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	holder, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect holder: %v", err)
	}
	defer holder.Close(ctx)

	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	if _, err := holderTx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrateAdvisoryLockKey); err != nil {
		t.Fatalf("holder acquire lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Migrate(context.Background()) }()

	select {
	case <-done:
		t.Fatal("Migrate returned before the advisory lock was released — lock not enforced")
	case <-time.After(300 * time.Millisecond):
		// Expected: still blocked on the holder's lock.
	}

	if err := holderTx.Rollback(ctx); err != nil {
		t.Fatalf("release holder lock: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Migrate after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Migrate did not complete after lock release")
	}
}

// TestMigrate_ConcurrentBootsAllSucceed mirrors the 8-app fleet cold-start
// scenario from a genuinely cold schema: the tables migrateSQL creates
// are dropped first, then N independent Store connections all call Migrate
// at once. Without serialization, concurrent CREATE TABLE/INDEX IF NOT
// EXISTS DDL against the same objects can race two sessions past the
// existence check before either commits, producing a duplicate-key/deadlock
// error that aborts that node's boot. All N must succeed.
func TestMigrate_ConcurrentBootsAllSucceed(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}
	const fleetSize = 8 // matches scripts/deploy-fleet.sh's FLEET roster

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coldSchema, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect cold schema reset: %v", err)
	}
	defer coldSchema.Close(ctx)
	for _, table := range []struct {
		name string
		sql  string
	}{
		{name: "storm_reports", sql: "DROP TABLE IF EXISTS storm_reports CASCADE"},
		{name: "weather_events", sql: "DROP TABLE IF EXISTS weather_events CASCADE"},
	} {
		if _, err := coldSchema.Exec(ctx, table.sql); err != nil {
			t.Fatalf("drop cold schema table %s: %v", table.name, err)
		}
	}

	stores := make([]*Store, fleetSize)
	for i := range stores {
		s, err := New(ctx, dsn)
		if err != nil {
			t.Fatalf("connect store %d: %v", i, err)
		}
		defer s.Close()
		stores[i] = s
	}

	errCh := make(chan error, fleetSize)
	for _, s := range stores {
		s := s
		go func() { errCh <- s.Migrate(ctx) }()
	}
	for i := 0; i < fleetSize; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent Migrate %d failed: %v", i, err)
		}
	}
}

// TestMigrate_LockTimeoutErrorsAfterConfiguredWait proves a second Migrate
// call gives up with a clear, actionable error after migrateLockTimeout
// (15s) rather than hanging forever when another session holds the
// advisory lock indefinitely — a WEDGED (not crashed) holder, which is
// exactly the scenario a crashed-connection auto-release does NOT cover.
// This is the fix for the fleet-wide-boot-outage risk: without a
// lock_timeout, one wedged node's Migrate call would block every other of
// the 8 fleet apps' boots forever (review amendment, most important fix
// in this plan — see this task's "bounded lock wait" design decision).
func TestMigrate_LockTimeoutErrorsAfterConfiguredWait(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}
	// Generous outer bound: must comfortably exceed migrateLockTimeout
	// (15s) plus connection/query overhead. Only bounds setup/connect —
	// the actual Migrate call under test uses context.Background() (see
	// below), matching production's undeadlined boot ctx exactly, so the
	// 15s bound proven here is coming ENTIRELY from lock_timeout, not from
	// this test's own ctx racing it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	holder, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect holder: %v", err)
	}
	defer holder.Close(ctx)

	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	defer func() { _ = holderTx.Rollback(ctx) }()
	if _, err := holderTx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrateAdvisoryLockKey); err != nil {
		t.Fatalf("holder acquire lock: %v", err)
	}
	// Deliberately never released during this test — simulates a wedged
	// (not crashed) holder, the exact scenario migrateLockTimeout exists
	// for. holderTx's deferred Rollback at test end is cleanup, not part
	// of the scenario under test.

	start := time.Now()
	err = s.Migrate(context.Background()) // mirrors main.go's undeadlined boot ctx
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected Migrate to return an error once the lock wait times out, got nil")
	}
	if !strings.Contains(err.Error(), "timed out waiting for fleet migration lock after 15s") {
		t.Fatalf("expected a clear timed-out-waiting-for-fleet-migration-lock error, got: %v", err)
	}
	if elapsed < 14*time.Second {
		t.Fatalf("expected Migrate to wait roughly the full 15s lock_timeout before giving up, only waited %v", elapsed)
	}
	if elapsed > 25*time.Second {
		t.Fatalf("expected Migrate to give up at ~15s, not hang well past it: %v", elapsed)
	}
}
