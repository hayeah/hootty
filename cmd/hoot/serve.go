package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hayeah/hootty"
)

// cmdServe is the API-only HTTP server that powers the example webui.
//
// It is a fan-out / multiplexer over a state-dir of managed
// sessions: each session has its own rpc.sock running the hootty
// library's mux (/state, /events, /attach, /pty/*), and `serve`
// exposes a session-keyed HTTP+WS surface on top of it.
//
// Routes (mounted both at the bare path and under `--prefix` so the
// same binary works behind a strip-prefix=false proxy and direct):
//
//	GET    /sessions                  list { sessions: [{...StateFile, alive}] }
//	POST   /sessions                  spawn (body: {cmd?, argv?, key?})
//	GET    /sessions/{key}            state.json (alias of /state)
//	DELETE /sessions/{key}            SIGTERM the hootty by pid
//	GET    /sessions/{key}/state      state.json
//	GET    /sessions/{key}/events     SSE proxy of upstream /events
//	GET    /sessions/{key}/attach     WebSocket bridge to upstream /attach
//	GET    /sessions/{key}/attach-raw HTTP/1.1 Upgrade pass-through (CLI)
//	GET    /healthz                   liveness
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", "", `listen address in Go net.Listen form (e.g. "127.0.0.1:20000", ":20000", "[::1]:20000")`)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	prefix := fs.String("prefix", "", `optional path prefix (e.g. "/api"); routes are mounted at both bare and prefixed paths`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bind == "" {
		return fmt.Errorf("--bind is required (e.g. --bind 127.0.0.1:20000)")
	}
	if err := os.MkdirAll(*stateDir, 0o755); err != nil {
		return fmt.Errorf("mkdir state-dir: %w", err)
	}

	store := hootty.NewStore(*stateDir)
	srv := &serveState{store: store, stateDir: *stateDir}

	mux := http.NewServeMux()
	register(mux, *prefix, "/sessions", srv.handleSessions)
	register(mux, *prefix, "/sessions/{key}", srv.handleSession)
	register(mux, *prefix, "/sessions/{key}/state", srv.handleState)
	register(mux, *prefix, "/sessions/{key}/events", srv.handleEvents)
	register(mux, *prefix, "/sessions/{key}/attach", srv.handleAttach)
	register(mux, *prefix, "/sessions/{key}/attach-raw", srv.handleAttachRaw)
	register(mux, *prefix, "/healthz", srv.handleHealth)

	fmt.Fprintf(os.Stderr, "hoot serve: listening on http://%s (state-dir=%s)\n", *bind, *stateDir)
	return http.ListenAndServe(*bind, mux)
}

// register mounts a handler at the bare path and (if prefix is
// non-empty) at <prefix><path> too. Lets a dev hit
// http://127.0.0.1:<port>/sessions directly even while devport
// proxies /api/sessions with strip_prefix=false.
func register(mux *http.ServeMux, prefix, path string, h http.HandlerFunc) {
	mux.HandleFunc(path, h)
	if prefix != "" {
		mux.HandleFunc(prefix+path, h)
	}
}

type serveState struct {
	store    *hootty.Store
	stateDir string
}

// handleSessions multiplexes GET / POST on /sessions.
func (s *serveState) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getSessions(w, r)
	case http.MethodPost:
		s.createSession(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// getSessions returns every session in the state-dir, with an
// `alive` flag derived from the per-session dir flock.
func (s *serveState) getSessions(w http.ResponseWriter, _ *http.Request) {
	states, err := s.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type entry struct {
		*hootty.StateFile
		Alive bool `json:"alive"`
	}
	out := struct {
		Sessions []entry `json:"sessions"`
	}{Sessions: make([]entry, 0, len(states))}
	for _, st := range states {
		out.Sessions = append(out.Sessions, entry{
			StateFile: st,
			Alive:     s.store.IsAlive(st.Hootty.Key),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleSession routes per-session methods on /sessions/{key}.
// GET delegates to handleState; DELETE goes to closeSession.
func (s *serveState) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleState(w, r)
	case http.MethodDelete:
		s.closeSession(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleState returns one session's state.json.
func (s *serveState) handleState(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	st, err := s.store.Load(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// handleHealth is a trivial liveness probe — used by devport's
// http health-check on the api route.
func (s *serveState) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":         true,
		"state_dir":  s.stateDir,
		"started_at": time.Now(),
	})
}
