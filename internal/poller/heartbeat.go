// heartbeat.go — the dead-man's-switch gating decision.
//
// A heartbeat ping alone only proves "poll() reached the end of the
// function" — it does NOT prove the mode-relevant work this node exists to
// do actually succeeded. Per-step failures (a single NWS 503, a hung SPC
// endpoint) are already logged and are non-fatal to the cycle by design
// (the next cycle in ~30s naturally retries) — but if EVERY cycle's
// mode-relevant step were failing while the process stayed up and
// looping, an unconditional end-of-cycle ping would keep healthchecks.io
// green forever, masking exactly the "process alive, accomplishing
// nothing" incident class this task's goal statement names.
// shouldHeartbeat is the single place that decides whether a given
// cycle's outcome is trustworthy enough to report as healthy.
package poller

// shouldHeartbeat reports whether this cycle's heartbeat should fire,
// given this node's Mode and the two independently-tracked outcomes:
//
//   - alertsOK: the NWS alerts fetch AND its DB upsert both succeeded this
//     cycle. SPC (storm-report) fetch failures do NOT factor in — they
//     already fail independently and non-fatally in pollStormReports, and
//     alerts are this project's actual safety-critical feed.
//   - mergedPublishOK: the merged-snapshot Publish call succeeded this
//     cycle. Per-state PublishState failures do NOT factor in — the
//     merged file is the back-compat/"view all" fallback every client can
//     use, so it is the publisher's actual safety-critical output.
//
// Only the outcome(s) relevant to this node's Mode gate the ping: an
// ingest-only node is judged solely on alertsOK, a publish-only node
// solely on mergedPublishOK, and a "both" node (single-node/local-dev
// deployments) on both. A phase this Mode doesn't run at all is vacuously
// fine — callers pass true for that phase's outcome (see poll()).
func shouldHeartbeat(mode Mode, alertsOK, mergedPublishOK bool) bool {
	ok := true
	if mode.ShouldIngest() {
		ok = ok && alertsOK
	}
	if mode.ShouldPublish() {
		ok = ok && mergedPublishOK
	}
	return ok
}
