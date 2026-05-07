package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hayeah/hootty"
)

// cmdServe is the API-only HTTP server that powers the example webui.
//
// It is a fan-out / multiplexer over a state-dir of managed
// sessions: each session has its own rpc.sock running the session
// library's mux (/state, /events, /attach, /pty/*), and `serve`
// exposes a session-keyed HTTP+WS surface on top of it.
//
// Routes (mounted both at the bare path and under `--prefix` so the
// same binary works behind a strip-prefix=false proxy and direct):
//
//	GET    /sessions                  list { sessions: [{...StateFile, alive}] }
//	POST   /sessions                  spawn (body: {cmd?, argv?, key?})
//	GET    /sessions/{key}            state.json (alias of /state)
//	POST   /sessions/{key}/clone      spawn sibling from session argv/cwd
//	GET    /sessions/{key}/resolve    resolve full key or unique prefix
//	POST   /sessions/{key}/signal     forward {"signal": ...} to rpc.sock
//	POST   /sessions/{key}/input      forward bytes (?paste=on|off) to rpc.sock /pty/input
//	DELETE /sessions/{key}            SIGTERM the session by pid
//	GET    /sessions/{key}/state      state.json
//	GET    /sessions/{key}/events     SSE proxy of upstream /events
//	GET    /sessions/{key}/attach     WebSocket bridge to upstream /attach
//	GET    /sessions/{key}/attach-raw HTTP/1.1 Upgrade pass-through (CLI)
//	GET    /healthz                   liveness
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", "", `listen address (e.g. "127.0.0.1:20000", ":20000", "[::1]:20000", "unix:/path/to/serve.sock")`)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	prefix := fs.String("prefix", "", `optional path prefix (e.g. "/api"); routes are mounted at both bare and prefixed paths`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bind == "" {
		return fmt.Errorf("--bind is required (e.g. --bind 127.0.0.1:20000 or unix:/path/to/serve.sock)")
	}
	if err := os.MkdirAll(*stateDir, 0o755); err != nil {
		return fmt.Errorf("mkdir state-dir: %w", err)
	}

	store := session.NewStore(*stateDir)
	srv := &serveState{store: store, stateDir: *stateDir}

	mux := http.NewServeMux()
	register(mux, *prefix, "/sessions", srv.handleSessions)
	register(mux, *prefix, "/sessions/{key}", srv.handleSession)
	register(mux, *prefix, "/sessions/{key}/resolve", srv.handleResolve)
	register(mux, *prefix, "/sessions/{key}/events", srv.handleEvents)
	register(mux, *prefix, "/sessions/{key}/attach", srv.handleAttach)
	register(mux, *prefix, "/sessions/{key}/attach-raw", srv.handleAttachRaw)
	// Catch-all for every other per-session verb. Specific routes
	// above (e.g. /sessions/{key}/attach) still win in http.ServeMux
	// because the path-shape with a literal segment is more specific
	// than the {path...} wildcard. This handler will gradually absorb
	// the per-verb forwarders above as they are deleted.
	register(mux, *prefix, "/sessions/{key}/{path...}", srv.handleSessionProxy)
	register(mux, *prefix, "/healthz", srv.handleHealth)

	ln, cleanup, isUnix, err := listenServeBind(*bind)
	if err != nil {
		return err
	}
	defer cleanup()

	server := &http.Server{Handler: mux}
	var stopSignals func()
	if isUnix {
		stopSignals = watchShutdownSignals(server)
		fmt.Fprintf(os.Stderr, "hoot serve: listening on unix:%s (state-dir=%s)\n", strings.TrimPrefix(*bind, "unix:"), *stateDir)
	} else {
		stopSignals = func() {}
		fmt.Fprintf(os.Stderr, "hoot serve: listening on http://%s (state-dir=%s)\n", *bind, *stateDir)
	}
	defer stopSignals()
	err = server.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func listenServeBind(bind string) (net.Listener, func(), bool, error) {
	if !strings.HasPrefix(bind, "unix:") {
		ln, err := net.Listen("tcp", bind)
		if err != nil {
			return nil, nil, false, err
		}
		return ln, func() { _ = ln.Close() }, false, nil
	}

	sockPath := strings.TrimPrefix(bind, "unix:")
	if sockPath == "" {
		return nil, nil, true, fmt.Errorf("--bind unix: requires a socket path")
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return nil, nil, true, fmt.Errorf("mkdir socket dir: %w", err)
	}
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, nil, true, fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, nil, true, err
	}
	cleanup := func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}
	return ln, cleanup, true, nil
}

func watchShutdownSignals(server *http.Server) func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(done)
	}
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
	store    *session.Store
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
		*session.StateFile
		Alive bool `json:"alive"`
	}
	out := struct {
		Sessions []entry `json:"sessions"`
	}{Sessions: make([]entry, 0, len(states))}
	for _, st := range states {
		out.Sessions = append(out.Sessions, entry{
			StateFile: st,
			Alive:     s.store.IsAlive(st.Session.Key),
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
