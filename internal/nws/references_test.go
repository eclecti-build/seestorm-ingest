package nws

import (
	"encoding/json"
	"testing"
)

// A reference object in the NWS feed carries both `@id` (the URL form) and
// `identifier` (the bare urn:oid: that matches our stored nws_id). PR2 matches
// on `identifier`; this test pins that the field lands where we expect.
func TestAlertProperties_DecodesReferences(t *testing.T) {
	raw := `{
		"event": "Tornado Warning",
		"messageType": "Update",
		"references": [
			{
				"@id": "https://api.weather.gov/alerts/urn:oid:2.49.0.1.840.0.aaa.003.1",
				"identifier": "urn:oid:2.49.0.1.840.0.aaa.003.1",
				"sender": "w-nws.webmaster@noaa.gov",
				"sent": "2026-05-31T02:49:00-07:00"
			}
		]
	}`

	var p AlertProperties
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.References) != 1 {
		t.Fatalf("want 1 reference, got %d", len(p.References))
	}
	if got := p.References[0].Identifier; got != "urn:oid:2.49.0.1.840.0.aaa.003.1" {
		t.Errorf("identifier = %q, want the bare urn:oid form", got)
	}
	if got := p.References[0].ID; got != "https://api.weather.gov/alerts/urn:oid:2.49.0.1.840.0.aaa.003.1" {
		t.Errorf("@id = %q, want the URL form", got)
	}
}

// A message with no references decodes to an empty slice (drives the no-op path).
func TestAlertProperties_NoReferences(t *testing.T) {
	var p AlertProperties
	if err := json.Unmarshal([]byte(`{"event":"Flood Warning"}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.References) != 0 {
		t.Errorf("want 0 references, got %d", len(p.References))
	}
}
