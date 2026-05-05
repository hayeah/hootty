package hootty

import (
	"encoding/json"
	"time"
)

// StateFile is the root of state.json. Two namespaced sections:
// hootty (owned by the hootty library) and state (owned by
// the Service — formerly called "service" in the pre-refactor
// schema; renamed to free up "service" as a Go type name in the
// consumer layer).
type StateFile struct {
	Hootty HoottyState     `json:"hootty"`
	State  json.RawMessage `json:"state,omitempty"`
}

// HoottyState is written by the hootty library. Services
// never touch it — this is purely the library's own bookkeeping.
//
// PID is the __hoot process's PID, used by external killers
// (e.g. `agentboss kill <id>`) to deliver SIGTERM to the right
// process without needing to know which PTY backend is in play.
type HoottyState struct {
	Key       string    `json:"key"`
	PID       int       `json:"pid,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
