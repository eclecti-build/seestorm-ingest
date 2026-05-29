package poller

import (
	"fmt"
	"strings"
)

// Mode selects which phases of the poll cycle a process runs. It lets the
// fleet split responsibilities: many region-scoped ingesters that write alerts
// into the shared database, and exactly one publisher that reads that database
// and writes the merged + history snapshots to R2. Running every node as a
// publisher (the default "both") multiplies history writes by the node count
// and collapses the client's history window — see the 2026-05 regional-rollout
// incident, where 8 publishing nodes turned a 30s history cadence into ~5s.
type Mode string

const (
	// ModeBoth polls upstreams AND publishes snapshots. Default; correct for
	// local dev and any single-node deployment.
	ModeBoth Mode = "both"
	// ModeIngest polls NWS/SPC and upserts to the database but publishes
	// nothing. Used by the region-scoped ingest nodes.
	ModeIngest Mode = "ingest"
	// ModePublish reads the shared database and publishes snapshots to R2 but
	// performs no upstream polling. Exactly one node should run this.
	ModePublish Mode = "publish"
)

// ParseMode resolves the MODE env value to a Mode. Empty defaults to ModeBoth
// so existing single-node deployments and local runs are unaffected. An
// unrecognized value is a hard error rather than a silent fallback, because
// defaulting a typo'd publisher back to "both" would reintroduce the
// history-amplification bug this flag exists to prevent.
func ParseMode(raw string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ModeBoth):
		return ModeBoth, nil
	case string(ModeIngest):
		return ModeIngest, nil
	case string(ModePublish):
		return ModePublish, nil
	default:
		return "", fmt.Errorf("invalid MODE %q (want one of: ingest, publish, both)", raw)
	}
}

// ShouldIngest reports whether this mode polls upstreams and writes to the DB.
// The empty Mode (zero value) behaves as ModeBoth so a Config built without an
// explicit Mode keeps the original poll+publish behavior.
func (m Mode) ShouldIngest() bool {
	return m == "" || m == ModeBoth || m == ModeIngest
}

// ShouldPublish reports whether this mode publishes snapshots to R2. The empty
// Mode (zero value) behaves as ModeBoth.
func (m Mode) ShouldPublish() bool {
	return m == "" || m == ModeBoth || m == ModePublish
}
