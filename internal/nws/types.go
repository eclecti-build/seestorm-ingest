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
}
