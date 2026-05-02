package supervisor

import (
	"encoding/json"
	"time"
)

// StateFile is the root of state.json. Two namespaced sections:
// supervisor (owned by the supervisor library) and state (owned by
// the Service — formerly called "service" in the pre-refactor
// schema; renamed to free up "service" as a Go type name in the
// consumer layer).
type StateFile struct {
	Supervisor SupervisorState `json:"supervisor"`
	State      json.RawMessage `json:"state,omitempty"`
}

// SupervisorState is written by the supervisor library. Services
// never touch it — this is purely the library's own bookkeeping.
//
// PID is the __supervise process's PID, used by external killers
// (e.g. `agentboss kill <id>`) to deliver SIGTERM to the right
// process without needing to know which PTY backend is in play.
type SupervisorState struct {
	Key       string    `json:"key"`
	PID       int       `json:"pid,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
