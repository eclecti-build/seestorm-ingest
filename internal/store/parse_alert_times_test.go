package store

import (
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

func TestParseAlertTimes(t *testing.T) {
	t.Parallel()

	received := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name          string
		effective     string
		expires       string
		wantOK        bool
		wantEffective time.Time
		wantExpires   time.Time
	}{
		{
			name:          "both valid",
			effective:     "2026-07-08T11:00:00Z",
			expires:       "2026-07-08T13:00:00Z",
			wantOK:        true,
			wantEffective: time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC),
			wantExpires:   time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC),
		},
		{
			name:      "unparseable expires: skip",
			effective: "2026-07-08T11:00:00Z",
			expires:   "not-a-timestamp",
			wantOK:    false,
		},
		{
			name:      "empty expires: skip",
			effective: "2026-07-08T11:00:00Z",
			expires:   "",
			wantOK:    false,
		},
		{
			name:          "unparseable effective: falls back to receivedAt, keeps expires",
			effective:     "garbage",
			expires:       "2026-07-08T13:00:00Z",
			wantOK:        true,
			wantEffective: received,
			wantExpires:   time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			props := nws.AlertProperties{Effective: tc.effective, Expires: tc.expires}
			gotEffective, gotExpires, ok := parseAlertTimes(props, received)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if !gotEffective.Equal(tc.wantEffective) {
				t.Errorf("effectiveAt = %v, want %v", gotEffective, tc.wantEffective)
			}
			if !gotExpires.Equal(tc.wantExpires) {
				t.Errorf("expiresAt = %v, want %v", gotExpires, tc.wantExpires)
			}
		})
	}
}
