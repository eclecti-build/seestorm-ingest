package nws

import (
	"encoding/json"
	"testing"
)

// TestAlertProperties_PreservesGeocode is a regression test for the bug where
// `properties.geocode.SAME` was silently dropped at ingest because the field
// wasn't declared on the AlertProperties struct. Without this field, the
// downstream snapshot reader's "preferred SAME path" was dead code and every
// alert fell through to the area_desc regex fallback.
//
// If this test fails, multi-state filtering is broken in production.
func TestAlertProperties_PreservesGeocode(t *testing.T) {
	t.Parallel()

	// Realistic NWS payload shape — a tornado warning with SAME codes for
	// two Wisconsin counties and the corresponding UGC zone codes.
	raw := `{
		"id": "urn:oid:2.49.0.1.840.0.test",
		"event": "Tornado Warning",
		"areaDesc": "Dane, WI; Rock, WI",
		"geocode": {
			"SAME": ["055025", "055045"],
			"UGC": ["WIC025", "WIC045"]
		},
		"parameters": {
			"eventMotionDescription": ["TIME...MOT...LOC 2345Z 230DEG 35KT 4268 8895"]
		}
	}`

	var props AlertProperties
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		t.Fatalf("unmarshaling alert properties: %v", err)
	}

	if got, want := len(props.Geocode.SAME), 2; got != want {
		t.Fatalf("Geocode.SAME length: got %d want %d (raw: %+v)", got, want, props.Geocode)
	}
	if props.Geocode.SAME[0] != "055025" || props.Geocode.SAME[1] != "055045" {
		t.Errorf("Geocode.SAME values: got %v want [055025 055045]", props.Geocode.SAME)
	}
	if got, want := len(props.Geocode.UGC), 2; got != want {
		t.Errorf("Geocode.UGC length: got %d want %d", got, want)
	}

	// Round-trip: marshal, then unmarshal again, then verify SAME/UGC survive.
	// This is the path UpsertAlert -> GetActiveAlerts takes via the JSONB
	// `properties` column.
	marshaled, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("re-marshaling: %v", err)
	}
	var roundTripped AlertProperties
	if err := json.Unmarshal(marshaled, &roundTripped); err != nil {
		t.Fatalf("re-unmarshaling: %v", err)
	}
	if len(roundTripped.Geocode.SAME) != 2 || roundTripped.Geocode.SAME[0] != "055025" {
		t.Errorf("Geocode.SAME did not survive round-trip: got %v", roundTripped.Geocode.SAME)
	}
}

func TestIsValidStateCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code string
		want bool
	}{
		{"WI", true},  // a real state
		{"NY", true},  // east coast
		{"AK", true},  // Alaska
		{"DC", true},  // District of Columbia
		{"PR", true},  // Puerto Rico
		{"GU", true},  // Guam
		{"ZZ", false}, // not a real state — the bug we're guarding against
		{"AA", false}, // matches regex but invalid
		{"XX", false},
		{"wi", false}, // not uppercase
		{"W", false},  // too short
		{"WIS", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			if got := IsValidStateCode(tc.code); got != tc.want {
				t.Errorf("IsValidStateCode(%q) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}
