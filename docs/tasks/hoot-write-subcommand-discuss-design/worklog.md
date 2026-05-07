---
status: done
section: Hoot write subcommand — discuss design
slug: hoot-write-subcommand-discuss-design
mode: worktree
spec: spec.md
created: 2026-05-07T04:29:51Z
---

> ## Hoot write subcommand — discuss design
>
> ---
> status:
>   type: open
> ---
>
> Discussion / spec mode. Spawn a claude agent to research and draft `spec.md`. No code yet.
>
> ### Feature
>
> `hoot write` — send input data to the supervised PTY of a hoot session, programmatically. Goal: easy for agents (and humans in scripts) to drive a session without going through `hoot attach`. Picture `hoot write <session> "ls -l\n"` or piping a script: `cat input.txt | hoot write <session>`.
>
> ### Open design questions for `spec.md`
>
> The user has more questions than answers. Push back on whatever doesn't make sense; this is a real design discussion.
>
> **Underlying protocol surface:**
> - Currently the supervisor only accepts raw bytes via the attach `MsgInput` frames. Confirm — read `attach_handler.go::clientReadLoop` and `pty_libghostty.go` write path. Is there an existing path other than attach that writes to the master FD?
> - For `hoot write`, do we add a new endpoint (e.g. `POST /input`) on the rpc.sock mux, or open a one-shot attach-like connection that sends only `MsgInput` frames and exits? Tradeoffs.
> - PTY-write locking: today the master is written to from (a) the dispatcher's query auto-replies via `WithWritePty`, (b) attach client `MsgInput` frames in `clientReadLoop`. If `hoot write` adds a third writer, do we need explicit locking around `master.Write()`, or does the existing serialization (one-goroutine-per-attach + the dispatcher's serialization for queries) cover us? File:line, then a clear answer.
>
> **Send keys vs send raw bytes:**
> - Currently only raw bytes. Question: should `hoot write` accept raw bytes only, or also a "key" syntax (`Ctrl-C`, `Enter`, `Up`, `M-x`, etc.)?
> - The user is unsure about syntax: tmux uses its own modifier syntax (`C-a`, `M-x`, etc.), kitty has the kitty keyboard protocol's CSI u format, GNU Readline / Emacs has `C-x C-c`. **Survey what's most principled and widely accepted.** Specifically check:
>   - tmux's `send-keys` syntax (file:line in tmux source)
>   - kitty `kitten send-keys` (if it exists) and the kitty keyboard protocol's encoding
>   - xdotool's syntax
>   - tcell / termbox / crossterm key naming conventions in TUI libraries
>   - VT100/ANSI standards if any
> - Pick a recommendation. The user's instinct is "raw bytes as primary; a thin key-name shorthand is fine if the syntax is widely understood, but don't invent a new one."
>
> **Bracketed paste:**
> - The user wants graceful bracketed-paste support — i.e. when the inner program has DEC mode 2004 enabled (bracketed paste), `hoot write` should wrap its payload in `\e[200~ ... \e[201~` so the inner program sees it as a single paste.
> - How does hoot know whether bracketed paste is active? The libghostty terminal tracks DECSET 2004 state — there must be an accessor. Check `pty_libghostty.go` / `go-libghostty` formatter.go for whether mode state is queryable.
> - Fallback when mode state isn't available or paste is disabled: just send raw bytes.
> - Should this be automatic (default-on, detect mode), opt-in flag (`--paste`), or opt-out flag? Recommend.
> - Edge case: payload contains the bracketed-paste-end sequence — must escape or reject.
>
> **Newlines / line feed handling:**
> - Two competing conventions: programs that read line-by-line want `\n` (or whatever the cooked-mode line discipline rewrites). PTYs in cooked mode rewrite `\r` → `\n`; in raw mode they don't. Most TUIs run raw-ish.
> - Question: when the user pipes `cat foo.txt | hoot write …`, do we send the bytes verbatim (`\n`), or rewrite to `\r` (which is what shells expect on Enter)? Both have surprise modes.
> - Look at how `tmux send-keys` and `xdotool type` handle this. Make a recommendation.
>
> **CLI shape:**
> - `hoot write <session> [--paste|--no-paste] [--keys] [--input-file FILE] [DATA...]`
> - Stdin pipe behavior: if no positional args and stdin is a pipe, read from stdin.
> - Exit codes: 0 success, non-zero on transport errors / unknown session.
> - `--remote` support same as `hoot attach` / `hoot detach` / `hoot kill`.
>
> **Local + remote:**
> - Local: dial rpc.sock, POST or open the input frame stream.
> - Remote: through `hoot serve` proxy.
>
> ### Recommendation
>
> Based on the survey, pick:
> - The protocol surface (new endpoint vs attach-like).
> - The key-syntax (raw bytes only, or raw bytes + a specific named-key syntax).
> - Bracketed-paste auto vs flag.
> - Newline rewriting policy.
>
> Sketch the implementation surface (which files to touch, any new dependencies).
>
> ### Pre-plant rfc gate
>
> Don't write code. Produce `spec.md` answering the above with a recommendation. The human reviews before any implementation.
>
> - [ ] rfc: review spec.md (protocol + key-syntax + paste + newlines)
> - [ ] implement per spec

## Todos

- [x] read attach_handler.go / pty_libghostty.go / routes — inventory write paths
- [x] check go-libghostty for bracketed-paste mode accessor
- [x] survey send-keys syntaxes (tmux, kitty, xdotool, readline)
- [x] draft spec.md with recommendations + open questions
- [x] **rfc gate** — ticked at 05:44Z

### Phase: implementation

- [x] add `writeMu` to LibghosttyPTY, wrap `Write` and `WithWritePty` callback
- [x] add `BracketedPasteActive()` accessor on LibghosttyPTY
- [x] extend `handleInput` route: `?paste=on|off`, `\e[201~` rejection, 1 MiB cap, 409 on paste-when-disabled
- [x] write `cmd/hoot/encode.go` — `strconv.Unquote` wrapper with friendly `\e` hint
- [x] write `cmd/hoot/write.go` — flag parsing, source-resolution, local + remote dispatch
- [x] register `write` in `cmd/hoot/main.go`
- [x] add `/sessions/{key}/input` proxy in `cmd/hoot/serve_spawn.go` + route in `serve.go`
- [x] e2e: encoded happy path, raw stdin, --paste happy + error
- [x] README update with `hoot write` examples
- [x] populate ## Evidence with transcripts and tests passing

## Agent log
- 2026-05-07T(draft) — Spawned. Set up worktree at `.worktrees/001` via `boss checkout hootty`.
- 2026-05-07T(draft) — Investigation findings (see spec §Background): `/pty/input` route already exists at `pty_libghostty_routes.go:111`, mounted at `pty_libghostty.go:704`. Master FD has three writer call-sites today; none use a lock.
- 2026-05-07T(draft) — `term.ModeGet(libghostty.ModeBracketedPaste)` exists in go-libghostty `terminal.go:309` — bracketed-paste detection is feasible.
- 2026-05-07T(draft) — Drafted `spec.md`. Recommendations: extend `/pty/input` (no new endpoint); raw bytes default + tmux-style `--keys`; `--paste=off` default with `--paste`/`--paste=on`/`--paste=auto`; verbatim newlines (no rewriting); add `writeMu` per user's steer.
- 2026-05-07T(draft) — Status `blocked` on `rfc:` gate. Four open questions in spec §Open questions waiting on human.
- 2026-05-07T(revision) — Human pushback on `--keys` parse-mode flag and on baking timing into the subcommand. Revised spec: (a) single client-side encoding language (C escapes + `\<NAME>` named-key sugar), `--raw` for byte-faithful pipe; (b) dropped all delay/pause primitives — multi-step flows are an outer-program concern. Server-side `?keys=…` removed; only `?paste=…` remains. See spec §Design notes for the rationale. New open questions list in spec §Open questions.
- 2026-05-07T(revision-3) — Human further trimmed: drop `\<NAME>` sugar entirely; "pretty much like printf, WYSIWYG." Spec now: argv interpreted with C escapes only; stdin / `--input-file` verbatim; no `--raw` flag (input source picks the mode). Cheat sheet of common byte sequences in `--help` / README. Open questions reduced to two: `--paste` default; whether to keep octal `\NNN`.
- 2026-05-07T(revision-4) — Human pointed out: just use Go's own string-literal interpreter via `strconv.Unquote`. Spec rule becomes "argv = Go double-quoted string literal, stdin = raw bytes." Implementation drops to ~10 lines; we get the Go grammar (full `\xHH`, `\uXXXX`, `\UXXXXXXXX`, `\NNN`, letters, validation) for free. One small loss: no `\e` (Go grammar doesn't have it; cheat sheet says use `\x1b`). Multi-line via heredoc on stdin works as before — that's the natural multi-line path. Spec §3 / §6 / §Architecture / §Steps / §Design notes updated. Open questions list now: just (1) 1 MiB cap, (2) `--paste` default.
- 2026-05-07T(revision-5) — Human: "not a fan of paste auto. think just on or off. if can't paste (return an error)." Dropped `auto` entirely. `--paste` is now boolean; server returns 409 if receiver doesn't have DECSET 2004 enabled, CLI exits 2 with clear stderr. Spec §1 / §4 / §6 / §Architecture / §Verification / §Open questions updated. Only one open question left: 1 MiB body cap.
- 2026-05-07T05:45Z rfc ticked at 05:44Z; status -> working. Starting implementation. 1 MiB body cap chosen as the v1 default (judgment call, no human override; can bump if real use exceeds it).
- 2026-05-07T05:59Z phase-1 server-side support landed (8acd4e2). writeMu around master.Write, BracketedPasteActive accessor, /pty/input now supports ?paste=on with 409 fallback, 1 MiB cap, paste-end rejection. 8-writer atomicity test green.
- 2026-05-07T06:06Z implementation complete (8acd4e2 + 46239c8 + 8fc68ca on branch). Full suite green; e2e smoke against a real cat session covers argv-encode, stdin-verbatim, --input-file, --paste error path, bad-escape error. Evidence section populated. status: done.

## Boss log
- 2026-05-07T05:44Z ticked: rfc

## Evidence

### Commits on this branch

```
8fc68ca Document hoot write subcommand and /pty/input changes in README
46239c8 Add hoot write subcommand and /sessions/{key}/input proxy
8acd4e2 Add writeMu, BracketedPasteActive, --paste support on /pty/input
```

### Tests

Full suite green:

```
$ go test -count=1 ./...
ok  	github.com/hayeah/hootty	6.940s
ok  	github.com/hayeah/hootty/cmd/hoot	2.373s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	0.386s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.558s
```

New tests added (all passing):

- `pty_libghostty_input_test.go` — `BracketedPasteActive` toggle, every
  `handleInput` query-param branch, payload `\e[201~` rejection, 1 MiB
  cap, and an 8-writer/8 KiB atomicity test that proves `writeMu`
  prevents byte-level interleaving.
- `cmd/hoot/encode_test.go` — happy paths for the Go string-literal
  grammar (12 cases: `\xHH`, `\uXXXX`, `\UXXXXXXXX`, octal, all letters,
  argv-join semantics) and parse-error mapping (5 cases including the
  `\e` → `\x1b` hint).
- `cmd/hoot/write_test.go` — `writeLocal` happy path, `--paste` query,
  409 → exit 2 mapping, unknown session → exit 3, `serve` proxy
  forwarding (body + query verbatim), proxy 404, proxy 1 MiB cap,
  source-precedence table.

### End-to-end smoke (transcript at `tmp/130349_e2e-transcript.txt`)

Spawned a real session running `cat`, drove it via the new subcommand:

```
$ hoot write --state-dir $S smoke "hello world\r"            # argv + escapes
$ /tmp/wf.in: from-file ; hoot write --state-dir $S --input-file /tmp/wf.in smoke
$ printf 'piped\r' | hoot write --state-dir $S smoke         # stdin pipe
$ curl --unix-socket $S/smoke/rpc.sock http://unix/pty/text  # final screen
hello world
hello world
from-filepiped
from-filepiped
```

Error paths verified:

```
$ hoot write --state-dir $S --paste smoke "x\r"          # cat has no DECSET 2004
hoot write: bracketed paste not enabled on receiver
exit=2

$ hoot write --state-dir $S smoke 'oops\efoo'            # \e is not in Go's grammar
hoot write: invalid escape: invalid syntax (use \x1b for ESC; Go string literals do not support \e)
exit=2
```

All four scenarios from spec §Verification covered. Notes on the
`hello world` appearing twice: once via the kernel's PTY line-discipline
echo, once via `cat`'s normal stdout — expected behavior, not a bug.

## Trouble report

- The user steer "lock the pty write" arrived mid-investigation. The existing comment in `pty_libghostty.go:136` claims master writes are safe from any goroutine. That's true for ≤PIPE_BUF; not for the multi-KB paste workload `hoot write` introduces. Spec recommends locking; design note records the trade-off (lock held during a syscall on a possibly-blocking fd — pre-existing risk, but newly more reachable).
- Considered: should `--keys` parsing live client-side or server-side? Spec leaves both options on the table but leans client-side (parser duplication is the load-bearing concern). Listed as open question for human.
