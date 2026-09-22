// Package model contains durable trace capture and attachment metadata.
package model

import "time"

// Offsets are complete OTLP request boundaries in the native session spool.
type Offsets struct {
	Spans  int64 `json:"spans"`
	Events int64 `json:"events"`
}

// Boundary snapshots all known native sessions. An absent map entry is unknown,
// never zero: readers must skip a range whose endpoints are not both known.
type Boundary struct {
	Action  string             `json:"action"`
	At      time.Time          `json:"at"`
	Offsets map[string]Offsets `json:"offsets"`
}

// Capture exists only for recordings that opted in at start. It survives in the
// raw carrier so retries use the original byte windows, even after spool growth.
type Capture struct {
	Boundaries []Boundary `json:"boundaries"`
	Errors     []string   `json:"errors,omitempty"`
}
