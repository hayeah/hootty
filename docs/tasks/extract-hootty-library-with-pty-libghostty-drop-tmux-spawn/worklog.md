---
status: done
section: Extract hootty library with pty+libghostty (drop tmux spawn)
slug: extract-hootty-library-with-pty-libghostty-drop-tmux-spawn
mode: worktree
spec: spec.md
created: 2026-05-02T06:20:48Z
---

> ## Extract hootty library with pty+libghostty (drop tmux spawn)
>
> ---
> status:
>   type: open
> ---
>
> Extract the hootty code currently living at `~/github.com/hayeah/dotfiles/libs/hayeah-go/hootty` into a standalone library repo at `hayeah/hootty`. The point is to stop using tmux as a spawn backend — instead, spawn processes directly on a PTY backed by go-libghostty, tee the raw PTY output for replay, and expose terminal state through an HTTP API so consumers can render it however they want (plain text, HTML via the ghostty formatter, or raw VT for an attach client).
>
> Reference for the libghostty binding: `/Users/me/Dropbox/notes/2026-04-30/go-libghostty-guide_claude.md`.
>
> Library shape (focus here first):
> - spawn a process with a libghostty-backed PTY attached
> - tee reader records all raw PTY output (so we can pump it elsewhere later)
> - HTTP mux that can run as a standalone service OR be embedded into another Go http server, with endpoints for:
>   - plain text (ghostty formatter)
>   - HTML (ghostty formatter)
>   - raw VT (so an `attach` CLI can replay into a terminal)
>
> Example app — a simple `hoot` CLI:
> - launches a process with a target directory for its state files
> - serves the HTTP mux over a unix socket
>
> Tmux is not gone entirely — it's just no longer a spawn backend. We can still write a tiny `attach` CLI later that replays the recorded PTY log into a tmux pane. That's a follow-up, not part of this task.
>
> Explicitly out of scope (future work, do not design for these now):
> - multi-attachment window-size negotiation
> - remote HTTP access
> - agent-driven shell automation
>
> No RFC gate — go straight to implementation. Write a spec.md as you go for your own thinking, but don't park for review.
>
> - [ ] extract hootty lib to standalone repo, swap tmux spawn for pty+libghostty, ship the example `hoot` CLI

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### Phase 1 — repo skeleton
- [x] init `~/github.com/hayeah/hootty` repo, master baseline, boss checkout
- [x] write go.mod, Makefile, README skeleton

### Phase 2 — port library plumbing
- [x] copy atomic/state/store/flock/poll/tailfile/signals/eventbus/service/hootty_iface/hootty + tests
- [x] strip TmuxSpawn/Plugin/KillOnExit; rewrite imports to `github.com/hayeah/hootty`
- [x] `go test ./...` passes for the non-PTY bits

### Phase 3 — pty + libghostty
- [x] write `pty.go` interface
- [x] write `pty_libghostty.go` against mitchellh/go-libghostty (dispatcher + WithWritePty)
- [x] write `pty_libghostty_keys.go` (tmux name → libghostty Key+Mods)
- [x] write `recorder.go` (file-backed tee writer)
- [x] wire recorder into readLoop tee

### Phase 4 — http routes
- [x] `pty_libghostty_routes.go`: /pty/text, /pty/html, /pty/vt, /pty/stream, /pty/input, /pty/resize, /pty/send-keys
- [x] Runner.Run optional RegisterRoutes lift (already supports interface check)

### Phase 5 — hoot CLI
- [x] cmd/hoot/{main,run,hoot,service,common}.go

### Phase 6 — verification
- [x] unit tests for libghostty PTY (feed, dump, html, vt, send-keys literal, recorder roundtrip, key parser)
- [x] e2e smoke: `hoot run -- bash -c '...'` + curl all 7 endpoints
- [x] populate `## Evidence`

### Phase 7 — README + finalize
- [x] README: motivation, API, build setup, hoot usage, future work (covered in repo README at phase 1; verified accurate)

## Agent log
- 2026-05-02T06:24Z spec written; new repo hayeah/hootty inited+checked out; phase 1 todo seeded
- 2026-05-02T06:26Z phase 1 done: go.mod + Makefile (pkg-config) + README skeleton. commit 5652d48
- 2026-05-02T06:29Z phase 2 done: lib plumbing ported, tests pass with fakePTY (fe6f087)
- 2026-05-02T06:32Z phases 3+4 done: libghostty pty (dispatcher, WithWritePty, recorder tee) + 7 http routes; vet+test pass (eeb7cba)
- 2026-05-02T06:41Z all phases done; e2e smoke captured at tmp/133940_813-e2e.txt; status -> done (ebe5bca, branch extract-hootty-library-with-pty-libghostty-drop-tmux-spawn)

## Boss log

## Evidence

New repo: `~/github.com/hayeah/hootty` (worktree at `repos/github.com/hayeah/hootty`, branch `extract-hootty-library-with-pty-libghostty-drop-tmux-spawn`).

### Build + tests

```
$ cd ~/github.com/hayeah/hootty/.worktrees/000
$ make build
go build -o bin/hoot ./cmd/hoot
$ make test
go test ./...
ok  	github.com/hayeah/hootty	0.777s
?   	github.com/hayeah/hootty/cmd/hoot	[no test files]
```

Tests cover: store list/load/resolve, atomic writer, flock advisory exclusion, poll, eventbus, libghostty PTY formatters (plain/HTML/VT against a real PTY pair), recorder byte-roundtrip, tmux-style key name parser, literal-fallback SendKeys.

### E2E smoke transcript

Saved at `tmp/133940_813-e2e.txt`. Highlights below — child writes `hello-world`, a bold token, and a red token; we hit every endpoint and verify the recorder + state file.

```
$ ./bin/hoot run --state-dir /tmp/sv.fRuJ3Q --key demo -- \
    bash -lc 'echo hello-world; printf "\x1b[1mbold\x1b[0m\n"; \
              printf "\x1b[31mred\x1b[0m\n"; sleep 30'
hoot: session "demo" started (state-dir=/tmp/sv.fRuJ3Q)
demo

$ ls /tmp/sv.fRuJ3Q/demo/
pty.log  rpc.sock  state.json  hootty.log

$ curl -s --unix-socket .../rpc.sock http://x/pty/text
hello-world
bold
red

$ curl -s --unix-socket .../rpc.sock http://x/pty/html
<div style="font-family: monospace; white-space: pre;">hello-world
<div style="display: inline;font-weight: bold;">bold</div>
<div style="display: inline;color: var(--vt-palette-1);">red</div></div>

$ curl -s --unix-socket .../rpc.sock http://x/pty/vt | xxd | head -3
00000000: 6865 6c6c 6f2d 776f 726c 640d 0a1b 5b30  hello-world...[0
00000010: 6d1b 5b31 6d62 6f6c 641b 5b30 6d0d 0a1b  m.[1mbold.[0m...
00000020: 5b30 6d1b 5b33 383b 353b 316d 7265 641b  [0m.[38;5;1mred.

$ curl -s --unix-socket .../rpc.sock http://x/state | jq .state.state
"running"

$ xxd /tmp/sv.fRuJ3Q/demo/pty.log
00000000: 6865 6c6c 6f2d 776f 726c 640d 0a1b 5b31  hello-world...[1
00000010: 6d62 6f6c 641b 5b30 6d0d 0a1b 5b33 316d  mbold.[0m...[31m
00000020: 7265 641b 5b30 6d0d 0a                   red.[0m..

# Send Ctrl-C: child exits, hootty publishes exit state.
$ curl -sX POST --unix-socket .../rpc.sock \
    -d '{"keys":["C-c"]}' http://x/pty/send-keys -w "HTTP %{http_code}\n"
HTTP 204

$ cat /tmp/sv.fRuJ3Q/demo/state.json
{
  "hootty": {"key":"demo","pid":17585,...},
  "state": {"state":"exited","pid":17586,"exit_code":-1,"exited_at":"..."}
}
```

What this proves:
- spawn process on libghostty-backed PTY: child runs, state.json shows `state=running` with the child pid.
- tee reader: `pty.log` contains the raw VT byte stream (`\x1b[1m`, `\x1b[31m`).
- HTTP mux endpoints, all three rendering modes:
  - `/pty/text` → plain text (no escapes).
  - `/pty/html` → bold via `font-weight: bold`, red via `var(--vt-palette-1)`.
  - `/pty/vt` → self-contained ANSI replay (escapes preserved).
- live stream + raw input: `/pty/send-keys` encodes Ctrl-C through libghostty's KeyEncoder, the child receives SIGINT, the hootty publishes `state=exited` in `state.json` and tears down `rpc.sock`.

## Trouble report

- libghostty cgo build is fiddly: needs `mitchellh/go-libghostty` checked out *and* built (`make build` there) before `make build` in this repo can compile. Captured in the README; documented in `Makefile`'s `LIBGHOSTTY` knob. Nothing actionable beyond docs unless mitchellh ships a tagged release with prebuilt binaries — track upstream.
- libghostty has no semver tag, so `go.mod` carries a pseudo-version and the worktree relies on a gitignored `go.work` for the `replace` directive. Once upstream tags, drop the `go.work`.
- libghostty's `MaxScrollback` defaults to 0 (no scrollback at all). Surprising; documented in design notes and pinned to 10_000 in `NewLibghosttyPTY`.
- Worker's slog leaked into the captured PTY stream on the first smoke run because the worker's stderr is the PTY slave. Fixed by redirecting `slog.Default()` to `<state>/<key>/hootty.log` in `cmd/hoot`. Library-side users who embed the Runner in their own server need to be aware of this if they ever route a worker process's stdio onto a PTY (rare; documented in design notes).
- Test races: master.Read → dispatcher → format is async with respect to test goroutines, so a single round-trip can race the readLoop's feed action. Tests poll FormatText on a 2s deadline. Could be cleaned up by adding a synchronous `Sync()` primitive to `LibghosttyPTY` if this comes up again.
- #friction: `boss checkout` assumes the target repo already exists (with at least one commit on master). Since this task creates a brand-new repo, I had to `git init`/baseline-commit/`boss checkout` in two steps. Not blocking but worth a note for boss UX.
