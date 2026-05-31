package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

// execer is the subset of pgx shared by *pgxpool.Pool and pgx.Tx, so retirement
// runs the same way inside the batch transaction and on the pool-based fallback.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// referencesByEventType groups the prior message ids referenced by a batch under
// the event_type of the message doing the referencing (PR2 Decision 3 — the
// retire predicate gates on matching event_type). Empty identifiers and
// reference-less alerts are skipped. Returns a non-nil (possibly empty) map.
func referencesByEventType(alerts []nws.Alert) map[string][]string {
	out := make(map[string][]string)
	for _, a := range alerts {
		for _, r := range a.Properties.References {
			if r.Identifier == "" {
				continue
			}
			out[a.Properties.Event] = append(out[a.Properties.Event], r.Identifier)
		}
	}
	return out
}

// retireReferenced soft-deletes every referenced prior row whose event_type
// matches the superseding message's, via execer (a tx or the pool). Idempotent
// (WHERE retired_at IS NULL) and a no-op when a referenced id is absent. Returns
// the number of rows retired.
func retireReferenced(ctx context.Context, q execer, alerts []nws.Alert) (int64, error) {
	var total int64
	for eventType, ids := range referencesByEventType(alerts) {
		tag, err := q.Exec(ctx, retireByReferenceSQL, ids, eventType)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}
