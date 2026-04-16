package publisher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

// Snapshot is the CDN-cacheable JSON published after each poll cycle
type Snapshot struct {
	GeneratedAt time.Time                  `json:"generated_at"`
	Area        string                     `json:"area"`
	AlertCount  int                        `json:"alert_count"`
	Alerts      []store.ActiveAlertGeoJSON `json:"alerts"`
}

type FilePublisher struct {
	dir string
}

func NewFilePublisher(dir string) *FilePublisher {
	return &FilePublisher{dir: dir}
}

func (p *FilePublisher) Publish(area string, alerts []store.ActiveAlertGeoJSON) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return fmt.Errorf("creating snapshot dir: %w", err)
	}

	snapshot := Snapshot{
		GeneratedAt: time.Now().UTC(),
		Area:        area,
		AlertCount:  len(alerts),
		Alerts:      alerts,
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshaling snapshot: %w", err)
	}

	// Atomic write: write to temp file then rename
	tmpPath := filepath.Join(p.dir, "active-events.json.tmp")
	finalPath := filepath.Join(p.dir, "active-events.json")

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp snapshot: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("renaming snapshot: %w", err)
	}

	return nil
}
