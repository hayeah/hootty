package session

import (
	"encoding/json"
	"time"
)

// StateFile is the root of state.json. Two namespaced sections:
// session (owned by the session library) and state (owned by
// the Service — formerly called "service" in the pre-refactor
// schema; renamed to free up "service" as a Go type name in the
// consumer layer).
type StateFile struct {
	Session SessionState    `json:"session"`
	State   json.RawMessage `json:"state,omitempty"`
}

// SessionState is written by the session library. Services
// never touch it — this is purely the library's own bookkeeping.
//
// PID is the __session process's PID, used by external killers
// (e.g. `agentboss kill <id>`) to deliver SIGTERM to the right
// process without needing to know which PTY backend is in play.
type SessionState struct {
	Key       string    `json:"key"`
	PID       int       `json:"pid,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
