package nws

import "encoding/json"

// AlertsResponse is the GeoJSON FeatureCollection from NWS /alerts/active
type AlertsResponse struct {
	Type     string  `json:"type"`
	Features []Alert `json:"features"`
}

// Alert is a single NWS alert feature
type Alert struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Properties AlertProperties `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"` // Preserve raw GeoJSON geometry
}

type AlertProperties struct {
	ID          string `json:"id"`
	Event       string `json:"event"`
	Severity    string `json:"severity"`
	Urgency     string `json:"urgency"`
	Headline    string `json:"headline"`
	Description string `json:"description"`
	Instruction string `json:"instruction"`
	AreaDesc    string `json:"areaDesc"`
	SenderName  string `json:"senderName"`
	Effective   string `json:"effective"`
	Expires     string `json:"expires"`
	Status      string `json:"status"`
	MessageType string `json:"messageType"`
	Category    string `json:"category"`
	Response    string `json:"response"`
	// Parameters carries the NWS `properties.parameters` map. Motion is
	// published here as `eventMotionDescription` (a single-element string
	// slice), which supersedes the legacy TIME...MOT...LOC block that
	// api.weather.gov no longer emits in `description`.
	Parameters map[string][]string `json:"parameters"`
	// Geocode mirrors the structured `properties.geocode` object NWS attaches
	// to every alert. Must be marshaled into the `properties` JSONB column
	// so the snapshot reader can derive `states[]` from SAME codes (preferred)
	// instead of regex-parsing `area_desc`. Without this field declared on
	// the struct, json.Marshal silently drops the upstream geocode and the
	// SAME-derivation path becomes dead code.
	Geocode AlertGeocode `json:"geocode"`
}

// AlertGeocode is the subset of `properties.geocode` we use today. SAME
// codes drive per-alert state derivation; UGC codes are kept for future
// zone-level filtering. Defined as its own type so consumers (store,
// snapshot reader, tests) can reference it directly.
type AlertGeocode struct {
	SAME []string `json:"SAME"`
	UGC  []string `json:"UGC"`
}
