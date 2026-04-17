// Package publisher distributes snapshot JSON to one or more destinations.
// Destinations implement the Publisher interface. Multi composes them so the
// poller can fan out to local disk + R2 without coupling to either.
package publisher

import (
	"context"
	"log/slog"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

// SnapshotKey is the canonical object/file name for the active events snapshot.
// Changing it is a breaking change for the public Worker allowlist.
const SnapshotKey = "active-events.json"

// Snapshot is the CDN-cacheable JSON published after each poll cycle.
type Snapshot struct {
	GeneratedAt time.Time                  `json:"generated_at"`
	Area        string                     `json:"area"`
	AlertCount  int                        `json:"alert_count"`
	Alerts      []store.ActiveAlertGeoJSON `json:"alerts"`
}

// Publisher writes a Snapshot somewhere durable and reachable.
// Implementations log their own success/failure with destination context.
type Publisher interface {
	Publish(ctx context.Context, snapshot Snapshot) error
}

// Multi fans out a snapshot to every registered publisher. A failure in one
// destination never stops the others — durability of the local file should
// not hinge on R2 being up, and vice versa.
type Multi struct {
	publishers []Publisher
}

// NewMulti builds a composite publisher.
func NewMulti(publishers ...Publisher) *Multi {
	return &Multi{publishers: publishers}
}

// Publish forwards the snapshot to every registered publisher.
// Returns the first error encountered (all publishers still run).
func (m *Multi) Publish(ctx context.Context, snapshot Snapshot) error {
	var firstErr error
	for _, p := range m.publishers {
		if err := p.Publish(ctx, snapshot); err != nil {
			slog.ErrorContext(ctx, "publisher failed", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
