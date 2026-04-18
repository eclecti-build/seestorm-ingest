package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// File publishes the snapshot to the local filesystem using an atomic
// write (tmp file + rename). Useful for local dev and as an on-Fly debug
// artifact reachable via `flyctl ssh console`.
type File struct {
	dir string
}

// NewFile constructs a File publisher that writes snapshots into dir.
// The directory is created on first publish if it doesn't exist.
func NewFile(dir string) *File {
	return &File{dir: dir}
}

// Publish writes the merged snapshot atomically to <dir>/active-events.json.
func (p *File) Publish(ctx context.Context, snapshot Snapshot) error {
	start := time.Now()

	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return fmt.Errorf("creating snapshot dir: %w", err)
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshaling snapshot: %w", err)
	}

	tmpPath := filepath.Join(p.dir, SnapshotKey+".tmp")
	finalPath := filepath.Join(p.dir, SnapshotKey)

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp snapshot: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("renaming snapshot: %w", err)
	}

	slog.InfoContext(ctx, "snapshot published",
		"destination", "file",
		"path", finalPath,
		"size_bytes", len(data),
		"alert_count", snapshot.AlertCount,
		"duration", time.Since(start),
	)

	return nil
}

// PublishState writes a per-state snapshot atomically to
// <dir>/active-events/<STATE>.json. Subdirectory is created on first call.
func (p *File) PublishState(ctx context.Context, snapshot StateSnapshot) error {
	start := time.Now()

	// The per-state subdir lives under <dir>/active-events/, mirroring the
	// R2 prefix layout so flycssh-console inspection matches what's in R2.
	stateDir := filepath.Join(p.dir, PerStateKeyPrefix)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating per-state snapshot dir: %w", err)
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshaling state snapshot: %w", err)
	}

	key := PerStateKey(snapshot.AreaState)
	tmpPath := filepath.Join(p.dir, key+".tmp")
	finalPath := filepath.Join(p.dir, key)

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp state snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("renaming state snapshot: %w", err)
	}

	slog.InfoContext(ctx, "state snapshot published",
		"destination", "file",
		"path", finalPath,
		"size_bytes", len(data),
		"alert_count", snapshot.AlertCount,
		"area_state", snapshot.AreaState,
		"duration", time.Since(start),
	)

	return nil
}
