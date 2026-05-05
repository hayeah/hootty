package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"

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

	sockPath, err := spawnSession(s.stateDir, key, argv)
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

// spawnSession forks `hoot __session --state-dir <d>
// --key <k> -- <argv...>` as a detached grandchild, mirroring
// cmdRun (run.go) but without the foreground attach. Returns the
// rpc.sock path once it appears.
func spawnSession(stateDir, key string, argv []string) (string, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return "", fmt.Errorf("pty.Open: %w", err)
	}
	defer master.Close()
	defer slave.Close()
	// The webui will issue a resize as soon as it attaches; 80x24
	// is a sane default for the brief window before that.
	if err := pty.Setsize(slave, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		return "", fmt.Errorf("setsize: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	hootArgs := []string{
		"__session",
		"--state-dir", stateDir,
		"--key", key,
		"--",
	}
	hootArgs = append(hootArgs, argv...)

	cmd := exec.Command(self, hootArgs...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.ExtraFiles = []*os.File{master}
	// Setsid: session in its own session. See run.go for why.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("fork hoot: %w", err)
	}
	// Don't wait on the grandchild; the serve process should not
	// reap it.
	_ = cmd.Process.Release()

	sockPath := filepath.Join(stateDir, key, "rpc.sock")
	if err := waitForSocket(sockPath, 3*time.Second); err != nil {
		return "", err
	}
	return sockPath, nil
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
