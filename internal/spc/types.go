package spc

import "time"

// StormReport represents a single storm report from SPC
type StormReport struct {
	Time      time.Time `json:"time"`
	Magnitude string    `json:"magnitude"`
	Location  string    `json:"location"`
	County    string    `json:"county"`
	State     string    `json:"state"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	Comments  string    `json:"comments"`
	Type      string    `json:"type"` // tornado, hail, wind
}
