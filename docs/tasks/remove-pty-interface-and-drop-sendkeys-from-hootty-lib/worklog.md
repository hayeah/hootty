---
status: done
section: Remove PTY interface and drop SendKeys from session lib
slug: remove-pty-interface-and-drop-sendkeys-from-session-lib
mode: worktree
spec:
created: 2026-05-02T07:42:45Z
---

> ## Remove PTY interface and drop SendKeys from session lib
>
> ---
> status:
>   type: open
> ---
>
> In `~/github.com/hayeah/hootty`, drop the `type PTY interface { ... }` abstraction now that tmux is no longer a spawn backend — there's only one implementation (`LibghosttyPTY`). Use the concrete type directly throughout the library and CLI.
>
> While we're at it, remove `SendKeys` (and the `/pty/send-keys` route + the tmux-style key-name parser). It was a holdover from the tmux era and is the wrong primitive: callers should `Write` raw bytes into the PTY. If we want ergonomics later, we can add a small standalone util that translates command-string aliases to raw byte sequences — but that's not part of this task; for now `Write` (and `/pty/input`) is the only input surface.
>
> Update tests, README, and the `hoot` example CLI accordingly. Keep the e2e smoke working without the send-keys step.
>
> - [ ] drop PTY interface, remove SendKeys + /pty/send-keys + key-name parser, update tests + README + hoot CLI

## Todos
- [x] delete `pty.go` (PTY interface) and `pty_libghostty_keys.go` (key parser)
- [x] strip SendKeys + KeyEncoder from `pty_libghostty.go`; drop `/pty/send-keys` route
- [x] remove handleSendKeys from `pty_libghostty_routes.go`
- [x] switch `SessionConfig.PTY` / `Runner.PTY()` / `Session.PTY()` to concrete `*LibghosttyPTY`; drop the RegisterRoutes type-assertion (call directly)
- [x] update `hootty_test.go`: replace `fakePTY` with a real `*LibghosttyPTY` from `pty.Open()`
- [x] prune `pty_libghostty_test.go` (drop SendKeys + key-parser tests)
- [x] update README.md (remove send-keys row, refresh blurb)
- [x] `go vet ./...` and `go test ./...` clean
- [x] e2e smoke without send-keys (Ctrl-C via raw `/pty/input`)

## Agent log
- 2026-05-02 — landed everything in a single commit `8543a45` on branch
  `remove-pty-interface-and-drop-sendkeys-from-session-lib`
  (9 files, +53 / -296). Net deletion of two files (`pty.go`,
  `pty_libghostty_keys.go`) plus surgical strips elsewhere.
- Decision: `SessionConfig.PTY` is now `*LibghosttyPTY` (concrete),
  not an interface — matches the section directive. The fakePTY stub
  in `hootty_test.go` was the only thing the interface bought us
  in tests, and it's cheap to swap for a real `*LibghosttyPTY` built
  on a `pty.Open()` pair via a tiny `newTestPTY(t)` helper.
- Decision: `RegisterRoutes` stays as a method (it's still useful as a
  named seam for "wire up the /pty/* routes"), but `Runner.Run` now
  calls it directly on `r.cfg.PTY` instead of through a runtime
  type-assertion — there's no longer any other PTY type to dispatch on.

## Boss log

## Evidence

### `go test -count=1 -v ./...` (tail)
```
=== RUN   TestLibghosttyPTYFormatters
--- PASS: TestLibghosttyPTYFormatters (0.01s)
=== RUN   TestLibghosttyRecorderRoundtrip
--- PASS: TestLibghosttyRecorderRoundtrip (0.01s)
=== RUN   TestLibghosttyPTYWriteRoundtrip
--- PASS: TestLibghosttyPTYWriteRoundtrip (0.00s)
...
=== RUN   TestRunnerRunsServiceAndSignalsCancellation
--- PASS: TestRunnerRunsServiceAndSignalsCancellation (0.00s)
=== RUN   TestRunnerRejectsIncompleteConfig
--- PASS: TestRunnerRejectsIncompleteConfig (0.00s)
PASS
ok  	github.com/hayeah/hootty	0.295s
```

### `go vet ./...` and `go build`
Both clean. `bin/hoot` rebuilds with no diagnostics.

### `hoot` CLI e2e smoke (no send-keys, raw input only)
```
$ ./bin/hoot run --state-dir $SD --key smoke -- bash -c 'echo hello-from-child; sleep 5'
hoot: session "smoke" started (state-dir=/tmp/svsmoke.ePbJ)
smoke

$ curl -s --unix-socket $SD/smoke/rpc.sock http://x/pty/text
hello-from-child

$ curl -s --unix-socket $SD/smoke/rpc.sock http://x/state
{"session":{"key":"smoke","pid":23781,...},
 "state":{"state":"running","cmd":"bash -c echo hello-from-child; sleep 5","pid":23782,...}}

$ # /pty/send-keys is gone:
$ curl -s -o /dev/null -w "HTTP %{http_code}\n" -X POST --unix-socket $SD/smoke/rpc.sock \
       -d '{"keys":["a"]}' http://x/pty/send-keys
HTTP 404

$ # raw bytes via /pty/input still drive the child — Ctrl-C as 0x03:
$ curl -s -o /dev/null -w "HTTP %{http_code}\n" -X POST --unix-socket $SD/smoke/rpc.sock \
       --data-binary $'\x03' http://x/pty/input
HTTP 204

$ # Child got SIGINT, session exited, rpc.sock is gone:
$ curl -s --unix-socket $SD/smoke/rpc.sock http://x/state
(no such file or directory)
```

This is the proof the section asked for: send-keys is fully removed,
and Ctrl-C still works through `Write` / `/pty/input`.

## Trouble report
Nothing of note. The interface-to-concrete swap was mechanical; the
only real edit-of-judgment was deciding to build `newTestPTY(t)` on
top of `pty.Open()` rather than retain a no-op stub — the cost is one
extra real PTY pair per test that needed a Session (~5), which is
trivial.
