package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
//	cwd:  optional working directory for the spawned command,
//	      resolved on the server. Empty falls back to the server's
//	      own current directory.
//	env:  optional one-shot env overrides applied on top of the
//	      server's environment when forking the session.
type createSessionReq struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Key       string            `json:"key,omitempty"`
	CWD       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	NoHistory bool              `json:"no_history,omitempty"`
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
			// Both argv and cmd empty: fall back to the server's
			// default shell. This is the path `hoot @host` takes —
			// the remote `hoot serve` resolves $SHELL on the remote
			// box so the user gets their own login shell, not the
			// caller's.
			argv = []string{resolveDefaultShell()}
		} else {
			parsed, perr := splitCmd(body.Cmd)
			if perr != nil {
				http.Error(w, "parse cmd: "+perr.Error(), http.StatusBadRequest)
				return
			}
			argv = parsed
		}
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

	cwd, err := resolveServeCWD(body.CWD)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sockPath, err := spawnSessionFromSpec(s.stateDir, key, spawnSpec{
		Argv:         argv,
		CWD:          cwd,
		EnvOverrides: body.Env,
		Cols:         80,
		Rows:         24,
		NoHistory:    body.NoHistory,
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

// resolveServeCWD picks the working directory for a server-side spawn:
// empty falls back to the server's os.Getwd; a relative path is
// absolutized so state.json doesn't record a path that depends on the
// server's cwd at lookup time.
func resolveServeCWD(req string) (string, error) {
	if req == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getcwd: %w", err)
		}
		return cwd, nil
	}
	abs, err := filepath.Abs(req)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %w", req, err)
	}
	return abs, nil
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
