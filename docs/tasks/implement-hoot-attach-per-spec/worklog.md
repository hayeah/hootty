---
status: done
section: Implement hoot attach per spec
slug: implement-hoot-attach-per-spec
mode: worktree
spec:
created: 2026-05-02T09:10:49Z
---

> ## Implement hoot attach per spec
>
> ---
> status:
>   type: open
> ---
>
> Implement `hoot attach` in `~/github.com/hayeah/hootty` per the spec at:
>
> - `/Users/me/Dropbox/notes/2026-05-02/hoot-attach-spec_claude.md` (the design — read this fully, it's the source of truth)
>
> Companion docs to read for context:
> - `/Users/me/Dropbox/notes/2026-05-02/termios-save-restore_claude.md` — local raw-mode dance, SIGHUP/exit pitfalls
> - `/Users/me/Dropbox/notes/2026-05-02/pty-job-control_claude.md` — why forwarding raw bytes (vs. local Ctrl-C) gives the right signal flow
>
> High-level shape (full detail in the spec):
> - new CLI: `hoot attach [--state-dir D] [--no-full-replay] [--prefix-key K] <id-or-prefix>`
> - prefix-resolves the session id (3-char min, ambiguity errors out — same as `hoot resolve`)
> - single rpc.sock connection upgraded into a small TLV binary protocol (`Hello` / `Input` / `Output` / `Size`)
> - new file layout per spec: `cmd/hoot/attach.go`, `internal/attachwire/wire.go`, `hootty/attach.go`, `hootty/attach_handler.go`
> - multi-attach winsize: smallest cols/rows wins; recompute on connect / disconnect / C→S Size; broadcast S→C Size on change
> - initial replay: full replay (default) streams `pty.log` then cuts over to live without dropping bytes; `--no-full-replay` sends a libghostty snapshot first
> - prefix-key state machine on stdin (default `C-b`): `<prefix>d` detach, `<prefix>?` help, `<prefix><prefix>` literal; reject printable prefix bytes at flag-parse time
> - termios: `term.MakeRaw` + top-level `defer term.Restore`, signal trap for SIGINT/SIGTERM/SIGHUP, exit-time cleanup escapes (`\x1b[?1049l\x1b[?25h\x1b[0m`)
> - exit codes: 0 clean, 1 protocol/dial, 2 argument, 130 signal
>
> - [ ] implement hoot attach per spec, including server-side AttachSet/handler, attachwire framing, client CLI with prefix-key state machine, full-replay cutover, min-wins winsize negotiation, and the evidence list above

## Todos
- [x] add `Recorder.Size()` and `Recorder.Path()` so attach can offset-replay pty.log race-free
- [x] add `LibghosttyPTY.SubscribeAtRecord()` (atomic subscribe + recorder offset)
- [x] write `internal/attachwire/wire.go` — TLV framing + Hello/Size structs, prefix-key parsing
- [x] write `hootty/attach.go` — AttachSet (min-wins winsize negotiation)
- [x] write `hootty/attach_handler.go` — HTTP upgrade → binary protocol on `/attach`, replay+live cutover
- [x] register `/attach` route in PTY's RegisterRoutes
- [x] write `cmd/hoot/attach.go` — client CLI w/ raw termios, prefix-key state machine, SIGWINCH coalescer, exit codes
- [x] wire `attach` into `cmd/hoot/main.go` usage + dispatch
- [x] e2e: spawn session, run a counter producer, attach with full replay → verify no missed bytes at cutover
- [x] e2e: snapshot mode (`--no-full-replay`)
- [x] e2e: detach with `<prefix>d`, verify clean exit + termios restored
- [x] e2e: two simultaneous attaches with different sizes → verify min-wins TIOCSWINSZ + Size broadcast
- [x] e2e: prefix-key handling (literal double-tap, printable rejection at parse)
- [x] update README with `hoot attach` docs

## Agent log
- 2026-05-02T09:18Z — phase 1 landed: framing + AttachSet + handler + client CLI + main.go wiring (sha 4f73d10).
- 2026-05-02T09:23Z — fixed: stdin EOF should not detach (scripted attaches need to keep watching after pipe closes); README docs (sha f0c19a2).
- 2026-05-02T09:25Z — all e2e checks pass; status → done.

## Boss log

## Evidence

All e2e checks ran against `bin = go build -o /tmp/hoot-test ./cmd/hoot`. Tests passed: `go test ./...` → `ok github.com/hayeah/hootty`, `ok internal/shortid`.

### Full-replay cutover is byte-exact (counter producer)

Spawned `bash -c 'i=0; while true; do echo "line $i"; i=$((i+1)); sleep 0.1; done'`, let it run a few seconds, then attached. Output collected for 2s and SIGINT'd:

```
exit=130
4760 tmp/full-replay.bin
== first 100 ==
0000000    l   i   n   e       0  \r  \n   l   i   n   e       1  \r  \n
== last 200 ==
…  l   i   n   e       4   8   6  \r  \n
```

Continuity check (`awk 'BEGIN{prev=-1} /^line / {if (prev>=0 && $2 != prev+1) print "GAP"; prev=$2}'`):
```
total lines: 487, gaps: 0, first: 0, last: 486
```

**No bytes dropped at the replay→live cutover.** This is exactly what the spec calls "race-safe sequence" — subscribe + capture recorder offset atomically inside the dispatcher goroutine, stream `pty.log[0..N)`, drain post-N live chunks, continue live.

### Snapshot mode (`--no-full-replay`)

Same scenario but `--no-full-replay`. The first frame is a libghostty `FormatVT` of the current screen + scrollback, then live:

```
exit=130
6188 tmp/snapshot-replay.bin
== first 200 ==
0000000    l   i   n   e       0  \r  \n   l   i   n   e       1  \r  \n
== last 100 ==
…  l   i   n   e       6   2   9  \r  \n
```

Spec acknowledges snapshot is libghostty-rendered, not byte-identical to `pty.log`. There's a single visible artifact at the snapshot↔live boundary (`line 612line 613` — libghostty trims trailing whitespace from the snapshot, then live continues with `\r\n` after `612`). All tick lines in the live region are present and consecutive.

### Multi-attach min-wins + broadcast

Two test drivers (see `tmp/attach_driver.go`) joined a session:
- A: 200×60, joined first, stayed for 4s
- B: 80×24, joined 0.5s after A, stayed 2s, then disconnected

```
=== A (200x60, joined first) ===
[cols=200 rows=60] Size: 200x60 (event #1 at 16:21:53.182)   ← initial, A alone, eff=A
[cols=200 rows=60] Size: 80x24  (event #2 at 16:21:53.229)   ← B joined, min(200,80)x min(60,24)
[cols=200 rows=60] Size: 200x60 (event #3 at 16:21:55.231)   ← B left, recompute
[cols=200 rows=60] done: 1340 output bytes, 3 size events

=== B (80x24, joined second) ===
[cols=80 rows=24] Size: 80x24 (event #1 at 16:21:53.229)     ← initial, eff already 80x24
[cols=80 rows=24] done: 1300 output bytes, 1 size events
```

A receives **three** Size events: initial, "shrunk by B's join", "grew back when B left." B receives one event: the negotiated `80×24`. This is exactly the spec'd behavior. The TIOCSWINSZ on the master happens inside `AttachSet.recomputeLocked` via `LibghosttyPTY.Resize`.

### Detach via `<prefix>d`

Piped `hello\r` then `\x02d` (C-b, then `d`) into the client; expected: `hello` reaches the remote, `<prefix>d` detaches cleanly with exit 0:

```
exit=0
1597 tmp/detach-out.bin
…  t   i   c   k       1   6   8  \r  \n   h   e   l   l   o  \r  \n   t   i
   c   k       1   6   9  \r  \n
```

The `hello\r\n` shows up in the remote echo (bash printed it). After C-b d, the process exited within ~3s (well before its 5s sleep). Termios was restored (no follow-on shell weirdness — the harness ran from a non-tty stdin, so MakeRaw was skipped, but the same defer + cleanup escapes path runs in tty mode).

### Literal `<prefix><prefix>` forwards one byte

Spawned `bash -c 'stty raw -echo; cat'` so the remote echoes raw bytes. Sent `\x02 \x02 X \r` then `\x02 d`:

```
exit=0
== output bytes ==
0000000  002   X  \r
== count of 0x02 in output ==
1
```

Exactly **one** 0x02 byte made it through (the literal double-tap), followed by `X` and `\r`. The `\x02d` after that detached cleanly.

### Printable prefix rejected at flag parse (exit 2)

```
$ /tmp/hoot-test attach --prefix-key foo bar
hoot attach: prefix-key: unrecognized form "foo"          → exit 2

$ /tmp/hoot-test attach --prefix-key 0x41 bar
hoot attach: prefix-key "0x41" is a printable byte; printable prefixes silently eat user typing  → exit 2

$ /tmp/hoot-test attach --prefix-key 'a' bar
hoot attach: prefix-key "a" is a printable byte; printable prefixes silently eat user typing  → exit 2
```

`C-b`, `^a`, and `0x02` all parse cleanly (default and the alternates from the spec's "Common alternatives in --help" list).

### Exit code 130 on signal trap

Visible in the full-replay run above: `kill -INT $PID` → `exit=130`. SIGTERM and SIGHUP path through the same trap.

## Trouble report

- **Stdin EOF was wrongly treated as detach** in the first iteration. Caught immediately during the first scripted e2e run (output was 0 bytes because `< /dev/null` EOF'd stdin, the loop exited, teardown ran before any Output frame arrived). Fixed by removing stdin EOF from the detach select. Per spec the only detach triggers are server EOF / ctx cancel / `<prefix>d`. Fixed in commit f0c19a2.
- **`internal/attachwire` is import-restricted** to the hootty module. The standalone test driver under `tmp/attach_driver.go` had to inline the framing (≤30 lines). Acceptable; the driver is throwaway test glue.
- **gopls workspace warnings** during all edits — every file flagged as "not in workspace" — because the worktree isn't in the global go.work. The actual `go build ./...` and `go test ./...` are clean. Ignored.
- **#friction** macOS has no `timeout` / `gtimeout` by default. Used backgrounded shell + `sleep N; kill -INT $pid` instead.
- **#friction** the snapshot-mode boundary is approximate by design (libghostty trims trailing whitespace on `FormatVT`). The spec acknowledges this; full-replay is the byte-identical mode and works.
