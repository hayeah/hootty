package supervisor

import "net/http"

// Supervisor is the affordance surface a Service sees at runtime.
//
// A value implementing this interface is passed to Service.Run as
// its second argument; it lives only for the duration of that call.
// The concrete implementation is *Runner.
//
// Three affordances, each scoped to "infra the Service doesn't want
// to reimplement":
//
//   - UpdateState: atomically rewrite the `state` section of
//     state.json and fan out to SSE subscribers on /events. This is
//     how Services tell the outside world what they're doing.
//
//   - PTY: the *LibghosttyPTY configured at Supervisor construction
//     time. Services use it to capture the screen / write raw bytes
//     into the master.
//
//   - Mux: the library-owned http.ServeMux served on
//     <StateDir>/<Key>/rpc.sock. Default library routes: /state,
//     /events, plus the LibghosttyPTY's /pty/* routes. Services
//     may register additional routes (agentboss adds /lease,
//     /lease-release).
//
// Services do NOT receive a handle to the Runner or the library's
// internal state (Writer, flock, signal channels). Those are infra.
// If a future method would need to go here, the rule is: is it
// something every Service needs that a library-owned goroutine is
// uniquely positioned to provide? If yes, add it; if no, it belongs
// on the Service or on a concrete helper.
type Supervisor interface {
	// UpdateState atomically rewrites state.json's `state` section
	// and fans out to /events SSE subscribers.
	UpdateState(state any) error

	// PTY returns the session's terminal transport. Constant for
	// the lifetime of the Runner — chosen at construction time.
	PTY() *LibghosttyPTY

	// Mux is the library-managed http.ServeMux. Safe to register
	// additional handlers on during Service.Run. The library
	// serves this mux on <StateDir>/<Key>/rpc.sock.
	Mux() *http.ServeMux
}
