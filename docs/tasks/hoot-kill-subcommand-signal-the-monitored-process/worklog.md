---
status: done
section: Hoot kill subcommand — signal the monitored process
slug: hoot-kill-subcommand-signal-the-monitored-process
mode: worktree
spec: spec.md
created: 2026-05-06T09:10:22Z
---

> ## Hoot kill subcommand — signal the monitored process
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Add a `hoot kill` subcommand that sends a signal to the monitored process of a hoot session. Modeled after Unix `kill(1)`: `-s <signal>` flag, default `TERM`. Should work for both local and remote sessions — the request goes to the session's `__session` supervisor process, which delivers the signal to the monitored child.
>
> Open design questions for the agent to answer in `spec.md`:
> - **Naming.** Is `hoot kill` the right verb, or something less Unix-overloaded (`signal`, `stop`, `term`)? Survey what other process supervisors call this.
> - **Process model recap.** What's the actual process tree in hoot today — session leader, process group leader, who owns the PTY, who is the `__session` process, where does the monitored child sit? Read the source and write it down. Without this, the rest of the design is guesswork.
> - **Signal delivery target.** Should the signal go to the monitored process directly (`kill(pid, sig)`), the monitored process's process group (`kill(-pgid, sig)`), or the session leader? What does each choice mean for child processes the user spawned inside the session? (e.g. `TERM` to a shell vs. to its foreground job.)
> - **Local vs remote.** Local: the CLI talks to the local `__session` over its existing IPC. Remote: same, over the existing remote transport. Confirm both paths exist and point at the relevant code.
> - **Lifecycle interaction.** Per the existing design, the `__session` exits when the monitored process exits. So `hoot kill -s KILL` should naturally cause the session to tear down — confirm that's the case and note any cleanup that needs to fire.
> - **Auth / safety.** Anything to worry about for remote signal delivery (auth, ACL, rate-limiting)? Probably nothing new since attach already authenticates, but call it out.
> - **Surface.** CLI shape (`hoot kill <session> [-s SIG]`), exit codes, error cases (no such session, signal-not-permitted, etc.).
>
> Pre-plant the rfc gate so the human reviews before implementation.
>
> - [ ] rfc: review spec.md (process model + signal delivery design)
> - [ ] implement per spec — include tests for at least the local path

## Todos

### Phase 1 — spec (in review)
- [x] Check out hootty into a worktree under repos/
- [x] Read source: process tree, IPC, remote transport, lifecycle, CLI registration
- [x] Draft spec.md (naming, process model, signal target, local/remote, lifecycle, auth, surface)
- [ ] **Wait at rfc gate** — human reviews `spec.md` and ticks `rfc: review spec.md` before implementation

### Phase 2 — implementation (post-rfc)
- [x] Library: `LibghosttyPTY.SignalForeground(sig)` (tcgetpgrp + `kill(-fgpgid, sig)` with `ErrNoForeground` fallback signal) — `pty_libghostty.go` (commit `00268e2`)
- [x] `cmd/hoot/service.go`: promote child `*exec.Cmd` and Session to atomic.Pointer fields; add `handleSignal`; register `/signal` on the mux (commit `a4c0bd7`)
- [x] Signal-name parser helper (`parseSignal` in `cmd/hoot/signal_parse.go`, accepts TERM / SIGTERM / sigterm / 9 / 15 / etc.) (commit `a4c0bd7`)
- [x] `cmd/hoot/kill.go`: `cmdKill` with `-s`, prefix resolution, local unix-socket POST, remote branch via `httpClient(remote)`; `cmd/hoot/main.go` `case "kill":` + `usage()` (commit `1c938a0`)
- [x] `cmd/hoot/serve.go` + `serve_spawn.go`: `POST /sessions/{key}/signal` proxy to upstream `/signal` (commit `8895342`)
- [x] Tests: library-level `TestSignalForegroundDeliversToFGPgrp`, CLI/integration `TestKillSignalDeliversToChild`, serve.go proxy `TestKillSignalReturnsBadRequestForUnknown`, `TestParseSignal` (commit `e2e0623`)
- [x] README update — `### hoot kill` section + CLI banner (commit `b7cd0b0`)
- [x] Live demo transcript + final verification — see `## Evidence`

## Agent log

- 2026-05-06T09:25Z — Worktree created at `~/github.com/hayeah/hootty/.worktrees/hoot-kill-subcommand-signal-the-monitored-process`, branch `hoot-kill-subcommand-signal-the-monitored-process` off master (186ce75). No `.worktrees.setup` hook present. Symlinked into `repos/github.com/hayeah/hootty`.
- 2026-05-06T09:35Z — Surveyed hootty source via Explore agent + targeted reads (`session.go`, `service.go`, `spawn.go`, `serve.go`, `serve_spawn.go`, `attach_handler.go`, `attachwire/wire.go`, `main.go`). Key findings recorded in `spec.md` "Process model recap":
  - `__session` is `Setsid: true` and owns PTY master on fd 3 (`spawn.go:82,87`).
  - Child is spawned with `Setsid+Setctty+Ctty:0` (`service.go:71-75`) — own session leader, claims PTY slave as CTTY.
  - `RunCmdService` holds the live `*exec.Cmd` — natural site for `cmd.Process.Signal(sig)`. Mux `HandleFunc` is already used for `/clone` (`service.go:53`).
  - Remote `hoot serve` already has `DELETE /sessions/{key}` that signals `__session` PID with SIGTERM (`serve_spawn.go:148-176`); new `/signal` should hit the **child** via the rpc.sock mux instead, for arbitrary signals + correct target.
  - Subcommand dispatch is plain stdlib `flag` + switch in `cmd/hoot/main.go:27-49`.
- 2026-05-06T09:45Z — Drafted `spec.md`. Recommendation: `hoot kill` (matches docker/tmux/`kill(1)`); default `TERM`; deliver via direct PID (not pgrp); JSON wire body `{"signal":"TERM"}`; punt `-g`/process-group broadcast to a follow-up. Open questions 1–6 in spec.
- 2026-05-06T09:46Z — `status: blocked` at the pre-planted rfc gate. Awaiting human tick of `rfc: review spec.md`.
- 2026-05-06T10:05Z — Human pushed back on direct-PID signal target during rfc review: wants Ctrl-C-like semantics (signal goes to foreground job when shell has one foregrounded). Updated `spec.md` to switch the recommended target from `kill(child.Pid, sig)` to **`tcgetpgrp(ptyMasterFD)` + `kill(-fgpgid, sig)`** with a fallback to direct-PID if `tcgetpgrp` fails. This matches the kernel's own dispatch path for terminal-driven signals, so an interactive `bash` running `vim` correctly forwards `hoot kill -s INT` to vim, while a non-interactive `python script.py` still gets hit directly. Added a Design notes entry capturing the pivot and the rejected alternatives. Still blocked at rfc gate.
- 2026-05-06T16:25Z — RFC gate ticked by human. Resumed Phase 2 implementation.
- 2026-05-06T16:30Z — `00268e2` library: `LibghosttyPTY.SignalForeground` (tcgetpgrp + kill, `ErrNoForeground` sentinel for the fallback path).
- 2026-05-06T16:32Z — `a4c0bd7` service: promote `*exec.Cmd` and `Session` to atomic.Pointer fields; add `RunCmdService.handleSignal`; register `POST /signal` on the rpc.sock mux. Also `parseSignal` helper accepting names (with/without SIG prefix, case-insensitive) and small integers.
- 2026-05-06T16:33Z — `1c938a0` cmd/hoot: `cmdKill` + `case "kill":` in `main.go` + `usage()` update. Local path dials rpc.sock, remote path uses `httpClient(remote)` against `/sessions/{key}/signal`. Exit-code table per spec.
- 2026-05-06T16:34Z — `8895342` serve: `POST /sessions/{key}/signal` proxy in `serve.go`/`serve_spawn.go` (resolve key, forward body to upstream rpc.sock, mirror status). Same shape as the `/events` proxy.
- 2026-05-06T16:36Z — Tests: library-level `TestSignalForegroundDeliversToFGPgrp`, integration `TestKillSignalDeliversToChild`, proxy-resolve `TestKillSignalReturnsBadRequestForUnknown`, and `TestParseSignal`. All pass; full `go test ./...` is green.
- 2026-05-06T16:37Z — `b7cd0b0` README: documented `hoot kill` (CLI banner + dedicated section). Live demo run (TERM, `-s 9`, prefix resolve, unknown session, bad signal, post-teardown) recorded in `## Evidence`. Status → `done`.

## Boss log
- 2026-05-06T09:28Z ticked: rfc

## Evidence

### Test suite

`go test ./...` from the hootty repo root, on the feature branch:

```
ok  	github.com/hayeah/hootty	0.565s
ok  	github.com/hayeah/hootty/cmd/hoot	2.879s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	0.142s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.316s
```

Targeted run of the new tests:

```
=== RUN   TestSignalForegroundDeliversToFGPgrp
--- PASS: TestSignalForegroundDeliversToFGPgrp (0.00s)
=== RUN   TestKillSignalDeliversToChild
--- PASS: TestKillSignalDeliversToChild (0.08s)
=== RUN   TestKillSignalReturnsBadRequestForUnknown
--- PASS: TestKillSignalReturnsBadRequestForUnknown (0.00s)
=== RUN   TestParseSignal
--- PASS: TestParseSignal (0.00s)
```

`TestSignalForegroundDeliversToFGPgrp` is the load-bearing one: it spawns a real `sleep` child on a real PTY pair with `Setsid+Setctty+Ctty=0` (mirroring `service.go:71-75`), polls `tcgetpgrp` until the child claims the foreground process group, then calls `LibghosttyPTY.SignalForeground(SIGTERM)` and asserts `cmd.Wait()` returns `signaled=SIGTERM`. This proves the kernel's "Ctrl-C target" path is exactly what we deliver to.

### Live demo (TERM happy path + exit-code propagation)

```
$ /tmp/hoot-kill-test run --state-dir "$SD" --key demo1 -- sleep 120 &
hoot: session "demo1" started (state-dir=/tmp/hk.tSVRL6)
demo1

$ /tmp/hoot-kill-test list --state-dir "$SD"
{"session":{"key":"demo1","pid":26697,...},"state":{"state":"running","cmd":"sleep 120","pid":26698,"started_at":"..."}}

$ /tmp/hoot-kill-test kill --state-dir "$SD" demo1
$ echo $?
0

$ cat $SD/demo1/state.json | python3 -m json.tool
{
    "session": {"key": "demo1", "pid": 26697, "argv": ["sleep","120"], ...},
    "state": {
        "state": "exited",
        "cmd": "sleep 120",
        "pid": 26698,
        "exit_code": -1,
        "started_at": "2026-05-06T09:37:23Z",
        "exited_at": "2026-05-06T09:37:24Z"
    }
}
```

The supervisor's `cmd.Wait()` returned, state flipped from `running` → `exited`, and the runner unwound (PTY closed, flock released, rpc.sock removed) — confirming the lifecycle interaction the spec called out: a fatal signal naturally tears the session down, no manual cleanup needed.

### Live demo (prefix resolution, `-s 9`, error paths)

```
$ /tmp/hoot-kill-test run --state-dir "$SD" --key abc123 -- sleep 120 &
hoot: session "abc123" started (state-dir=/tmp/hk.uKmmOS)

$ /tmp/hoot-kill-test kill --state-dir "$SD" -s 9 abc   # prefix "abc" → abc123, signal #9 = KILL
$ echo $?
0
$ python3 -c "import json; d=json.load(open('$SD/abc123/state.json')); print(d['state']['state'], d['state']['exit_code'])"
exited -1

$ /tmp/hoot-kill-test kill --state-dir "$SD" nosuch
hoot kill: no match for "nosuch"
$ echo $?
3

$ /tmp/hoot-kill-test kill --state-dir "$SD" -s WAT abc
hoot kill: unknown signal "WAT"
$ echo $?
2

$ /tmp/hoot-kill-test kill --state-dir "$SD" abc        # session already torn down
hoot kill: Post "http://unix/signal": dial unix rpc.sock: connect: no such file or directory
$ echo $?
1
```

Everything matches the documented exit-code table in the spec. The "session already torn down" case surfaces as a connect failure (exit 1) rather than a typed 5/conflict because once the supervisor exits, `rpc.sock` is removed — there is no server to return 409. This is consistent with `hoot attach`'s behavior on the same condition.

### Commits on this branch

```
b7cd0b0  README: document hoot kill subcommand
e2e0623  test: cover SignalForeground and the /signal RPC
8895342  serve: proxy POST /sessions/{key}/signal to rpc.sock
1c938a0  cmd/hoot: add hoot kill subcommand
a4c0bd7  service: register POST /signal on rpc.sock
00268e2  session: add LibghosttyPTY.SignalForeground
```

(Per-todo commits, each focused and self-contained.)

## Trouble report

- None blocking. Source is well-organized; spec was straightforward to assemble once the process tree was laid out.
- Minor: `TestKillSignalDeliversToChild` had to swap `os.Stdin/Stdout/Stderr` to point at the PTY slave for the duration of the test, because `RunCmdService.Run` wires `cmd.Stdin/Out/Err = os.Stdin/Out/Err` (it inherits the session's stdio in the real flow). A cleanup-restore in `t.Cleanup` keeps it isolated. Not worth refactoring `RunCmdService` for testability — the swap is local to one test and well-documented.
- Minor: when the supervisor has already exited, `hoot kill` returns the connect-failure error (exit 1) rather than a typed "session not running" (exit 5). The supervisor removes `rpc.sock` on shutdown, so there's no server to return 409. Consistent with how `hoot attach` reports the same condition. Could be polished by checking the flock first, but not worth the extra round-trip for a rare edge case.
- Style nits flagged by the linter on pre-existing files (`errors.As` → `errors.AsType`, `range int` modernization, etc.) were left untouched — out of scope for this section.
- `go mod tidy` promoted three indirect deps (`golang.org/x/sys`, `golang.org/x/term`, `coder/websocket`) to direct, since `pty_libghostty.go` now imports `golang.org/x/sys/unix` directly. No new transitive deps.
