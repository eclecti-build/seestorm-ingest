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

// Publish writes the snapshot atomically to <dir>/active-events.json.
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
