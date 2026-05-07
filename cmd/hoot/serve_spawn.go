package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hayeah/hootty"
)

// createSessionReq is the JSON body of POST /sessions.
//
//	cmd:  shell-style string, tokenized with single/double-quote
//	      grouping (no escapes). First token = program, rest = argv.
//	argv: pre-tokenized []string alternative to cmd. argv wins if
//	      both are set, so callers that already have an array
//	      don't have to reason about quoting.
//	key:  optional — if provided and not currently alive, used as
//	      the session id. Otherwise generateKey picks one.
type createSessionReq struct {
	Cmd  string   `json:"cmd,omitempty"`
	Argv []string `json:"argv,omitempty"`
	Key  string   `json:"key,omitempty"`
}

// createSession is POST /sessions. It forks `hoot __session`
// directly from this process so the child session is our
// grandchild and the response can wait until rpc.sock appears
// (3s) — at which point the webui's optimistic row can be
// replaced with real data.
func (s *serveState) createSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	argv := body.Argv
	if len(argv) == 0 {
		body.Cmd = strings.TrimSpace(body.Cmd)
		if body.Cmd == "" {
			http.Error(w, "cmd (or argv) is required", http.StatusBadRequest)
			return
		}
		parsed, perr := splitCmd(body.Cmd)
		if perr != nil {
			http.Error(w, "parse cmd: "+perr.Error(), http.StatusBadRequest)
			return
		}
		argv = parsed
	}
	if len(argv) == 0 {
		http.Error(w, "cmd has no tokens", http.StatusBadRequest)
		return
	}
	key := body.Key
	if key == "" {
		generated, err := generateKey(s.store)
		if err != nil {
			http.Error(w, "generate key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		key = generated
	}
	if s.store.IsAlive(key) {
		http.Error(w, fmt.Sprintf("session %q already alive", key), http.StatusConflict)
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		http.Error(w, "getcwd: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sockPath, err := spawnSessionFromSpec(s.stateDir, key, spawnSpec{
		Argv: argv,
		CWD:  cwd,
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
		return
	}

	st, err := s.store.Load(key)
	if err != nil {
		http.Error(w, "load state after spawn: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type resp struct {
		*session.StateFile
		Alive      bool   `json:"alive"`
		SocketPath string `json:"socket_path"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp{
		StateFile:  st,
		Alive:      true,
		SocketPath: sockPath,
	})
}

func (s *serveState) handleClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	var body cloneSessionReq
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	resp, err := cloneSession(s.store, s.stateDir, key, body.Key, body.Env)
	if err != nil {
		writeCloneError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeCloneError(w http.ResponseWriter, err error) {
	if errors.Is(err, errCloneKeyAlive) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeResolveError(w, err)
}

// closeSession is DELETE /sessions/{key}. Sends SIGTERM to the
// session's pid; the library handles the rest (drain children,
// release flock, exit cleanly). Returns 204 once the signal is
// delivered (or already gone).
func (s *serveState) closeSession(w http.ResponseWriter, r *http.Request) {
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
	if st.Session.PID == 0 {
		http.Error(w, "state.json has no session pid", http.StatusConflict)
		return
	}
	proc, err := os.FindProcess(st.Session.PID)
	if err != nil {
		http.Error(w, "find process: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// ESRCH = already gone; treat as success.
		if !errors.Is(err, os.ErrProcessDone) {
			http.Error(w, "sigterm: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSignal is POST /sessions/{key}/signal. Resolves the key
// via the store (full id or unique prefix), then forwards the
// request body to the upstream rpc.sock's /signal route, mirroring
// status code and body back to the client.
func (s *serveState) handleSignal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.PathValue("key")
	if query == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	state, err := s.store.Resolve(query)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	sockPath := filepath.Join(s.stateDir, state.Session.Key, "rpc.sock")

	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return dialSock(ctx, sockPath)
		},
	}}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://unix/signal", io.NopCloser(strings.NewReader(string(body))))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstream.Header.Set("Content-Type", ct)
	} else {
		upstream.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(upstream)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleInput is POST /sessions/{key}/input. Resolves the key via
// the store (full id or unique prefix) and proxies the request body
// — including the `paste` query param — to the upstream rpc.sock's
// /pty/input route. Mirrors the dial pattern used by handleSignal.
func (s *serveState) handleInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.PathValue("key")
	if query == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	state, err := s.store.Resolve(query)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	sockPath := filepath.Join(s.stateDir, state.Session.Key, "rpc.sock")

	// 1 MiB matches the upstream cap; reading any further is
	// pointless because the upstream will reject it.
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > (1 << 20) {
		http.Error(w, "body too large (max 1 MiB)", http.StatusRequestEntityTooLarge)
		return
	}

	upstreamURL := "http://unix/pty/input"
	if raw := r.URL.RawQuery; raw != "" {
		upstreamURL += "?" + raw
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return dialSock(ctx, sockPath)
		},
	}}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstream.Header.Set("Content-Type", ct)
	} else {
		upstream.Header.Set("Content-Type", "application/octet-stream")
	}

	resp, err := client.Do(upstream)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleAttachments is DELETE /sessions/{key}/attachments — close
// every attachment on the session. Resolves the key prefix locally
// and proxies the DELETE through to the upstream rpc.sock.
func (s *serveState) handleAttachments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.proxyDelete(w, r, "/attachments")
}

// handleAttachmentByID is DELETE /sessions/{key}/attachments/{id} —
// close one attachment by its short id.
func (s *serveState) handleAttachmentByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing attachment id", http.StatusBadRequest)
		return
	}
	s.proxyDelete(w, r, "/attachments/"+id)
}

// proxyDelete resolves the key prefix and forwards a DELETE to the
// upstream rpc.sock at upstreamPath. Mirrors the dial pattern in
// handleSignal.
func (s *serveState) proxyDelete(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	query := r.PathValue("key")
	if query == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	state, err := s.store.Resolve(query)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	sockPath := filepath.Join(s.stateDir, state.Session.Key, "rpc.sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return dialSock(ctx, sockPath)
		},
	}}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, "http://unix"+upstreamPath, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := client.Do(upstream)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// splitCmd is a small shell-ish tokenizer: whitespace separates
// tokens, single- and double-quoted runs are grouped (with the
// quotes stripped). No backslash escapes — to embed a quote, use
// the other flavor. Good enough for `bash`, `bash -l`,
// `sh -c "echo hi | wc -l"`, `sh -c 'echo "hi"'`.
func splitCmd(s string) ([]string, error) {
	var out []string
	var cur []rune
	var quote rune

	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}

	for _, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			cur = append(cur, r)
			continue
		}
		switch {
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur = append(cur, r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c-quoted string", quote)
	}
	flush()
	return out, nil
}
