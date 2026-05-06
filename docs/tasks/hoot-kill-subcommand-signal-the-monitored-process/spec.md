# spec: `hoot kill` — signal the monitored process

## Goal

Add a `hoot kill` subcommand that delivers a Unix signal to the monitored child process of a hoot session. Modeled after `kill(1)`: positional `<session>` argument plus `-s <signal>` flag (default `TERM`). Same surface for local and remote sessions — both routes terminate at the session's `__session` process, which calls `cmd.Process.Signal(sig)` on the child it spawned.

Out of scope: process-group broadcast (`-g`), bulk kill across multiple sessions, signal-by-name aliases beyond the standard set, sending signals to `__session` itself (use `DELETE /sessions/{key}` or kill the pid).

## Process model recap (read this first)

The relevant code paths, all under `repos/github.com/hayeah/hootty/`:

- **`hoot run`** (`cmd/hoot/run.go`) re-execs itself as **`hoot __session`** (`cmd/hoot/spawn.go:38` — `spawnSessionFromSpec`).
  - `__session` is started with `SysProcAttr{Setsid: true}` (`spawn.go:87`). It becomes a new **session leader** and process-group leader. PTY slave is its stdin/stdout/stderr; the **PTY master is passed on fd 3** as `ExtraFiles[0]` (`spawn.go:82`).
  - `__session` writes `state.json`, opens `rpc.sock` in the same dir, holds a directory flock (`flock.go`/`atomic.go`), and runs an `http.ServeMux` on `rpc.sock` (`session.go:166-167, 251`).

- **The monitored child** is spawned by `RunCmdService.Run` (`cmd/hoot/service.go:48-109`) with:
  ```go
  cmd.SysProcAttr = &syscall.SysProcAttr{
      Setsid:  true,
      Setctty: true,
      Ctty:    0,
  }
  ```
  (`service.go:71-75`). The child becomes **its own session leader** and **claims the PTY slave as its controlling terminal**. `__session` itself stays attached to the slave but never calls `tcsetpgrp`, so the child's job-control machinery owns the foreground pgrp without `__session` getting EIO on master-side ioctls.
  - `cmd.Process.Pid` is recorded in `state.json` (`service.go:90`). The `*exec.Cmd` lives inside the `RunCmdService` instance; `cmd.Process.Signal(sig)` is the obvious in-process signal primitive.
  - On child exit, `cmd.Wait()` returns (`service.go:95`), state flips to `exited`, `Run` returns, and `session.Run` defers release the flock and shut down the listener (`session.go:133, 143-144`). The session tears down naturally.

- **Process tree summary** (one running session):
  ```
  shell (user terminal)
   └─ hoot run             [exits once attach hands off]
       └─ hoot __session   [Setsid → session leader; owns PTY master fd 3; serves rpc.sock]
           └─ <user cmd>   [Setsid+Setctty → its own session leader; PTY slave is its CTTY]
               └─ … shell's foreground job, etc.
  ```

- **Local IPC**: `<state-dir>/<key>/rpc.sock` is a Unix socket. The library mux registers `/state` and `/events` (`session.go:166-167`); services may add more (e.g. `RunCmdService` adds `/clone`, `service.go:53`). The attach path uses an HTTP/1.1 Upgrade handshake (`attach_handler.go:62-102`); other RPCs are plain HTTP.

- **Remote transport**: `cmd/hoot/serve.go` mounts a multiplexer over a state dir of sessions. Routes include `GET /sessions`, `GET /sessions/{key}/state`, `GET /sessions/{key}/events`, `GET /sessions/{key}/attach`, and **`DELETE /sessions/{key}` which signals `__session` directly via `os.FindProcess(state.Session.PID)` + `proc.Signal(SIGTERM)`** (`cmd/hoot/serve_spawn.go:148-176`). SSH remotes (`internal/sshtransport/transport.go`) tunnel HTTP through `ssh -L`, so the same routes work.

## Architecture

### Where the signal call lives

The call site is **inside `RunCmdService` in `cmd/hoot/service.go`**, because that's the place that holds both the live `*exec.Cmd` (for the fallback path) and (via the session) access to the PTY master fd. We add a new mux route `POST /signal` registered alongside `/clone`:

```go
// service.go
super.Mux().HandleFunc("/signal", s.handleSignal)
```

`handleSignal` reads `{"signal": "TERM"}` from JSON body, parses the signal name (with or without `SIG` prefix; numeric also accepted), then:

1. Reads the PTY master fd from the session (a small accessor on the `Session` interface — to be added — that returns the master `*os.File` set up in `spawn.go:82`).
2. `fgpgid, err := unix.IoctlGetInt(int(masterFD), unix.TIOCGPGRP)` — read fg pgrp.
3. If err or `fgpgid <= 1`: fall back to `s.cmd.Process.Signal(sig)`.
4. Else: `syscall.Kill(-fgpgid, sig)`.

We need to keep a reference to `cmd` on the receiver — currently `Run` holds it as a local; promote to `s.cmd` (an `atomic.Pointer[exec.Cmd]` is sufficient since the only reader/writer pairs are `Run` and `handleSignal`).

Response: `204 No Content` on success; `409 Conflict` if child not yet running or already exited; `400` for unparseable signal; `403 Forbidden` is not applicable on Unix (the child shares uid; EPERM only happens for cross-uid signals which we don't have).

### Local CLI path

`cmd/hoot/kill.go` (new file) — `cmdKill`:

1. Parse flags: `-s <SIG>` (default `TERM`), `-state-dir`, `--remote`, `<session-or-prefix>` positional.
2. Resolve key via existing prefix-resolution helper (used by `attach`, `clone`).
3. **Local path** (no `--remote`): build a `*http.Client` over a unix-socket dialer pointed at `<state-dir>/<key>/rpc.sock` (the same pattern `serve_bridge.go:36-40` uses), POST `http://unix/signal` with body `{"signal":"TERM"}`.
4. Map response: 204 → exit 0; 404 (no /signal route — old session) → friendly error; 409 → exit 5; 400 → exit 2.

### Remote CLI path

Add **`POST /sessions/{key}/signal`** to `cmd/hoot/serve.go`'s mux:

```go
register(mux, *prefix, "/sessions/{key}/signal", srv.handleSignal)
```

Implementation in `cmd/hoot/serve_spawn.go` (next to `closeSession`) — `handleSignal` is a thin proxy: load state for the key (404 if missing), open `<state-dir>/<key>/rpc.sock`, forward the request body to upstream `/signal`, mirror the status code back. Same pattern as `handleEvents` in `serve_bridge.go:26-82`.

CLI client (`cmdKill`) for `--remote http(s)://…` and `ssh://…`: same as `cmd/hoot/list.go`'s use of `httpClient(remote)`, but POST to `/sessions/{key}/signal`. SSH transport tunnels via the existing `ssh -L` plumbing — no new code there.

### Signal delivery target — the PTY's foreground process group

We send the signal to the **current foreground process group of the PTY**, the same target the kernel uses for Ctrl-C / Ctrl-\ / Ctrl-Z. Mechanism: `__session` already owns the PTY master on fd 3 (`spawn.go:82`); call `unix.IoctlGetInt(masterFD, unix.TIOCGPGRP)` (i.e. `tcgetpgrp`) to read the current foreground pgid, then `syscall.Kill(-fgpgid, sig)`.

Why fg-pgrp, not direct PID:

- **Matches user intuition for an interactive session.** If the child is `bash` and the user has `vim` in the foreground, `hoot kill -s INT` should interrupt vim, not bash — exactly what Ctrl-C does. Direct delivery to the shell's pid would do almost nothing useful in interactive mode (bash ignores SIGINT at the prompt and doesn't auto-forward arbitrary signals).
- **Degrades gracefully for non-interactive children.** For `hoot run -- python script.py`, python is the session leader and the only process on the PTY, so its pgid IS the fg pgid; `kill(-pgid, sig)` ≡ `kill(pid, sig)` plus any subprocesses python put in its own pgrp. Same effect as direct PID, but also catches helper processes — usually what the user wanted.
- **One mechanism for all signals.** SIGINT/SIGQUIT/SIGTSTP/SIGTERM/SIGHUP/SIGUSR1 all behave like the terminal-driven version. SIGKILL on the fg pgrp still tears down the session: if the shell is foreground, killing it ends `cmd.Wait()` in `service.go:95` and the supervisor unwinds; if a job is foreground, only that job dies and the shell remains running, which is the same outcome as Ctrl-\ on a job.

Alternatives considered (kept for future reference; see Design notes for the pivot from #1 to #3):

1. **`kill(child.Pid, sig)` — direct to the supervised child.** Closest to typing `kill PID` from another shell. Loser: doesn't match terminal semantics; signaling an interactive bash with INT is essentially a no-op.
2. **`kill(-child.Pgid, sig)` — child's process group.** Since the child is `Setsid`, its pgid equals its pid, so this hits only the shell — same effective behavior as #1 for an interactive shell. No improvement.
3. **`tcgetpgrp(masterFD)` + `kill(-fgpgid, sig)` — fg pgrp of the PTY.** Picked. Matches Ctrl-C semantics exactly.
4. **Whole-session broadcast** (walk `/proc` or use `pkill -s <sid>`). Too aggressive; rejected.

Edge cases:

- **fg pgid changes between read and kill.** Race window is microseconds; if it shifts, we signal the previous fg group, which is no worse than the user typing Ctrl-C a moment earlier. Acceptable.
- **`tcgetpgrp` returns 0 or the kernel "no foreground" sentinel.** Rare; happens if the controlling terminal has been disowned. Fall back to `kill(child.Pid, sig)` so the call never silently no-ops.
- **`tcgetpgrp` returns the pgid of `__session` itself.** Shouldn't happen — `__session` never calls `tcsetpgrp`, only the child does (via Setctty + job control). If it does, falling back to `child.Pid` is also safe.

A future `--pid` (or `--leader`) flag can opt out and signal the child PID directly, for the rare case where the user really wants to hit the supervised process specifically. Not in v1.

### CLI shape

```
hoot kill [--remote <url>] [--state-dir <d>] [-s <SIG>] <session-or-prefix>
```

- `<SIG>` accepts `TERM`, `SIGTERM`, `9`, `KILL`, `INT`, `HUP`, `USR1`, `USR2`, `WINCH`, etc. Parse via a small lookup table covering the standard 1..31 + a few common high signals; case-insensitive; strip `SIG` prefix.
- Default signal: `TERM`.
- Positional `<session>` resolves via the same unique-prefix logic as `attach`.

Exit codes:

| code | meaning                                                     |
|------|-------------------------------------------------------------|
| 0    | signal delivered                                            |
| 2    | usage error (bad flag, unknown signal name, no session arg) |
| 3    | no such session / ambiguous prefix                          |
| 5    | session exists but child not running (already exited)       |
| 1    | other I/O / network error                                   |

(EPERM is folded into exit 1 with a clear message; it shouldn't happen for same-uid sessions.)

## Steps

1. Wire definition: pick on-the-wire shape for `POST /signal` (JSON body `{"signal": "TERM"}` returning 204/4xx). Document in a doc-comment on the handler.
2. `session.go` (library): add an accessor on `Session` to expose the PTY master fd / `*os.File` (or surface a `SignalForeground(sig)` helper that wraps `tcgetpgrp` + `kill(-fgpgid, sig)` directly — preferred, keeps the PTY fd encapsulated).
3. `cmd/hoot/service.go`: promote `cmd` to a field on `RunCmdService` (`atomic.Pointer[exec.Cmd]`); add `handleSignal` that calls the library helper, with a fallback to `s.cmd.Process.Signal(sig)` if `tcgetpgrp` fails or returns `<= 1`; register `/signal` on the mux next to `/clone`. Parse signal names with a small helper.
3. `cmd/hoot/kill.go`: implement `cmdKill` with `-s` flag, prefix resolution, local unix-socket POST.
4. `cmd/hoot/main.go`: add `case "kill":`; update `usage()` to document it.
5. `cmd/hoot/kill.go`: extend with `--remote` branch using `httpClient(remote)` POST to `/sessions/{key}/signal`.
6. `cmd/hoot/serve.go` + `cmd/hoot/serve_spawn.go`: register `/sessions/{key}/signal` and implement `handleSignal` as a unix-socket proxy to upstream `/signal`. Update the doc comment listing routes.
7. Tests:
   - Local: spawn a `sleep 60` session via `RunCmdService` in-process (the existing test harness already does this for clone tests), POST to `/signal` with TERM, assert child exits and state goes to `exited` with non-zero exit code. Repeat with KILL.
   - Local CLI: shell out to the binary in a temp state-dir, run `hoot run -- sleep 60 &`, then `hoot kill <key>`, assert wait completes.
   - Remote: smaller integration test that exercises the serve.go proxy against an in-process upstream rpc.sock. (Skip the full SSH path — same wire as HTTP.)
8. Update README / `usage()` text.

## Verification

- `go test ./...` from the hootty repo root, with new tests in the package(s) above.
- Live demo transcript: `hoot run -- bash`, then in another shell `hoot list` to find the key, then `hoot kill -s TERM <key>` and `hoot kill -s KILL <key>` (after re-spawning), pasted into `## Evidence` in worklog.
- `state.json` after kill should show `state: "exited"` with appropriate `exit_code` (143 for TERM, 137 for KILL on Linux; macOS varies).

## Open questions

1. **Default signal: `TERM`** — matches `kill(1)`. Confirm.
2. **Wire: JSON body** (`{"signal":"TERM"}`) vs. **query string** (`POST /signal?sig=TERM`) vs. **path** (`POST /signal/TERM`). Recommend JSON: easy to extend later (e.g. `{"signal":"TERM","group":true}` if we ever add pgrp delivery). Confirm.
3. **Pid-direct opt-out (`--pid` / `--leader`)**: ship in v1 or punt? Recommend punt — fg-pgrp is the right default; an opt-out for "signal the supervised process specifically regardless of fg state" can be added when someone has a concrete need.
4. **Auth on remote `/sessions/{key}/signal`**: the existing `DELETE /sessions/{key}` route on `hoot serve` has no auth; it's expected to be bound to localhost or behind an external proxy. Same posture for `/signal`. Note in code comment; no new mechanism unless requested.
5. **Symbol vs. number**: support both (`-s 9`, `-s KILL`)? Recommend yes — trivial, matches `kill(1)`.
6. **Naming**: `kill` vs. `signal` vs. `stop`. Survey:
   - `tmux` — `kill-session`, `kill-server`, `kill-window`. Uses "kill" generously.
   - `systemctl` — `systemctl kill --signal=TERM <unit>` and `systemctl stop`. Uses both.
   - `supervisorctl` — `supervisorctl signal HUP <name>` and `stop`/`start`. Uses "signal".
   - `docker` — `docker kill --signal=KILL <ctr>` and `docker stop` (TERM then KILL after grace).
   - `pm2` — `pm2 stop`, `pm2 sendSignal`. No "kill" verb.
   - **Recommendation: `hoot kill`**. Most aligned with `kill(1)` / docker / tmux. "Signal" is more accurate but verbose; "stop" implies graceful TERM and obscures the signal choice.

## Design notes

- 2026-05-06T10:05Z — **Flipped signal target from direct child PID (`kill(pid, sig)`) to the PTY's foreground process group (`tcgetpgrp` + `kill(-fgpgid, sig)`).**
  - What flipped: human reviewer pointed out that for an interactive shell session ("spawn a bash, foreground something"), the user expects the signal to land on the foreground job — i.e. the same target Ctrl-C uses. Direct PID delivery to the shell is essentially a no-op for SIGINT (bash ignores it at the prompt) and inconsistent for SIGTERM (shell-dependent forwarding behavior).
  - Mechanism: `__session` already holds the PTY master on fd 3; `unix.IoctlGetInt(masterFD, unix.TIOCGPGRP)` returns the kernel's current foreground pgid for that terminal — exactly the value the kernel itself dispatches Ctrl-C/Ctrl-\\/Ctrl-Z to. Then `syscall.Kill(-fgpgid, sig)`.
  - Alternatives revisited:
    - **Direct child PID** (original recommendation): closest to typing `kill PID` from another shell, but doesn't match terminal semantics. Loser on user-intuition grounds.
    - **Child's own pgrp** (`kill(-child.Pid, sig)`): since the child is `Setsid`, pgid == pid, so this is identical to direct-PID for an interactive shell. No improvement.
    - **fg pgrp via tcgetpgrp** (picked): matches Ctrl-C exactly; degrades gracefully to direct-pgrp behavior for non-interactive children where there's no shell-managed fg job.
    - **Whole-session broadcast**: too aggressive; rejected.
  - Fallback: if `tcgetpgrp` fails or returns `<= 1` (no controlling terminal / sentinel), fall through to `cmd.Process.Signal(sig)` so the call never silently no-ops.
  - Follow-on: a `--pid` / `--leader` flag could opt out of fg-pgrp delivery for users who really do want to hit the supervised child specifically (e.g. send SIGUSR1 to a daemon child while a shell is foreground). Not in v1; easy to add later by branching in `handleSignal`.
  - Implementation impact: needs a small library-level addition — either expose the PTY master fd on the `Session` interface, or (preferred) add a `SignalForeground(sig syscall.Signal) error` helper that encapsulates `tcgetpgrp` + `kill(-fgpgid, sig)` + fallback to a caller-provided pid.
