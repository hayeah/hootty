package session

import (
	"encoding/json"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

// Re-exported wire types so consumers of the session package don't
// have to reach into internal/attachwire just to read a state.json.
type (
	PTYSize = attachwire.PTYSize
	Origin  = attachwire.Origin
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
//
// Size is the currently-effective PTY size — i.e. AttachSet.Effective().
// When the set goes empty we keep the last value (matches AttachSet
// semantics). encoding/json doesn't honor `omitempty` for non-pointer
// struct values, so a zero Size renders as {"cols":0,"rows":0} during
// the brief pre-Hello window; that's harmless.
//
// Attachments lists everyone currently connected to /attach. The
// supervisor populates entries on Hello and removes them on serveOne
// teardown; the slice is `omitempty` so old state.json files (with no
// attachments key at all) round-trip cleanly.
type SessionState struct {
	Key         string             `json:"key"`
	PID         int                `json:"pid,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	Argv        []string           `json:"argv,omitempty"`
	CWD         string             `json:"cwd,omitempty"`
	Size        PTYSize            `json:"size"`
	NoHistory   bool               `json:"no_history,omitempty"`
	Attachments []AttachmentRecord `json:"attachments,omitempty"`
}

// AttachmentRecord is the supervisor's view of one connected
// attachment. ID is short (3-8 chars from shortid.IDAlphabet),
// minted against the session's own existing attachment IDs.
type AttachmentRecord struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	Size      PTYSize   `json:"size"`
	Origin    *Origin   `json:"origin,omitempty"`
}
