# `hoot write` — subcommand spec

Status: rfc draft (no code yet — gate at `- [ ] rfc: review spec.md`)
Owner: agent draft for human review.

## Goal

Add `hoot write <session> [DATA...]` so agents and shell scripts can drive a hoot
session's PTY without going through `hoot attach`. **Behaves like `printf`:**
positional args are interpreted with C-style backslash escapes, the resulting
bytes go to the PTY master verbatim. WYSIWYG — what you write is what gets sent.

`hoot write` is a **primitive**: encode bytes, send bytes, exit. Multi-step
flows (send → wait → observe → send) belong in an outer program / shell loop
that calls `hoot write` and `hoot text` repeatedly. We do not bake timing,
conditional waits, or expect-style observation into this subcommand.

Out of scope:

- Reading from the session (already covered by `/pty/text`, `/pty/stream`,
  `hoot attach`).
- Named-key vocabulary (`\<Up>`, `\<C-c>`, tmux-style tokens). Users who want
  Up arrow type `\x1b[A`; Ctrl-C is `\x03`. Bytes only.
- Mode-change side effects (no toggling DEC modes from the client).
- Timing / delay directives (`\p…`, `--pause`, etc.). The user composes
  scripts of `hoot write` + `sleep` + `hoot text` in their shell.
- Expect-style "wait for prompt" or screen-polling. Outer-program problem.
- `printf`-style `%` format specifiers. We do byte encoding, not formatted
  output of variables.

## Background — what already exists

The supervisor already has the route we need. Inventory of the master-FD write
paths (file:line):

- `pty_libghostty.go:141` — `WithWritePty` callback that the libghostty
  terminal invokes for query auto-replies (DA / DECRPM / cursor-pos / XTVERSION).
  Runs on the **dispatcher goroutine** because libghostty calls back from inside
  `term.VTWrite(...)`, which only ever runs inside `p.do(...)` (the dispatcher
  is the single owner of `p.term`, see `pty_libghostty.go:198-218`).
- `pty_libghostty.go:296-298` — `LibghosttyPTY.Write(data []byte)` is the
  exported Go-level entry. It is `master.Write` with no lock. Comment at
  line 136 says "Master writes are kernel-safe to do from any goroutine."
  That's true *up to* `PIPE_BUF` (≈512B on macOS, 4096B on Linux); larger
  writes from concurrent goroutines can interleave.
- `attach_handler.go:274` — each `MsgInput` frame in `clientReadLoop` calls
  `h.pty.Write(payload)`. Each attach connection gets its own clientReadLoop
  goroutine, so two attached clients are already two unlocked writers.
- `pty_libghostty_routes.go:111-127` — `handleInput`, mounted at
  `/pty/input` via `pty_libghostty.go:704`. **POST raw body → master.Write.**
  This already does ~95% of what `hoot write` needs.

Conclusion: there is *already* a non-attach path that writes to the master FD.
The new subcommand is mostly a CLI client + a small protocol extension.

## Recommendations

### 1. Protocol surface — extend the existing `/pty/input` route

Do **not** add a new endpoint. Do **not** open an attach-like upgraded
connection. `POST /pty/input` over the rpc.sock is exactly the right shape: one
round trip, body = raw bytes, 204 on success.

Server-side stays byte-pure. **All encoding parsing happens client-side**;
the server only ever sees the bytes the user wants on the master FD. The
sole server-side extension is bracketed-paste handling (which is a wire-level
property of the receiving terminal, not part of the encoding language).

Extensions to add to `handleInput`:

- Accept query param `?paste=on|off` (default `off`). No `auto`.
- When `paste=on`: query `term.ModeGet(ModeBracketedPaste)`.
  - If true: wrap body in `\x1b[200~ ... \x1b[201~`.
  - If false: return **409 Conflict** with body `bracketed paste not enabled
    on receiver`. The client surfaces this as a clear error and exits 2.
- Reject (400) payloads that contain `\x1b[201~` when `paste=on`.
- Hard cap: 1 MiB body. Bigger pastes are very rarely intentional and a
  runaway pipe is a denial-of-service vector against the supervisor.

No `?keys=…` query param. The encoding language is a client-side concern.

Why not a brand-new endpoint:

- `/pty/input` already exists, is already exercised by tests, and already does
  the lock-free `master.Write` we are now going to lock.
- A new `/input` would duplicate routing and mux wiring with no benefit.

Why not attach-like (one-shot upgrade conn that sends `MsgInput` frames):

- Attach is heavier: HTTP/1.1 Upgrade dance, registry persistence, snapshot
  prefix, recorder side-effects, plus you must invent an "I'm done, please
  disconnect" signal. None of that buys anything for a one-shot write.
- The attach wire format buys streaming. We do not need streaming — a single
  POST body is the whole input chunk.

### 2. PTY-write locking — add a write mutex around `master.Write`

Add `writeMu sync.Mutex` to `LibghosttyPTY`. Wrap the two real writers:

- `LibghosttyPTY.Write` (exported entry, used by attach + `/pty/input`):
  acquire `writeMu` for the full `master.Write(data)` call.
- The `WithWritePty` callback at `pty_libghostty.go:141`: also acquire
  `writeMu` for the duration of the inner `master.Write`. The callback runs
  on the dispatcher, but we still take the lock so it serializes against
  any in-flight attach / `hoot write` write.

This matches the user's steer: "we should lock the pty write, so concurrent
writes don't clobber each other." Each call becomes an atomic chunk regardless
of length, even when an attach client and a `hoot write` collide, or when an
attach client races with a libghostty query auto-reply.

Notes:

- The lock is held only across a single `master.Write` syscall, which is
  bounded by kernel write-buffer drain. The PTY master in nonblocking mode
  would short-write; check current state — if blocking, a misbehaving
  consumer that doesn't drain `master.Read` could stall this lock. (Today
  this is already the failure mode for any large attach paste, so adding a
  mutex doesn't make it worse, but flag for follow-up: short-write loop or
  a write deadline.)
- Document in `LibghosttyPTY.Write`'s doc comment: "Atomic per call; no
  goroutine-level interleaving."

### 3. Input encoding — argv is a Go string literal, stdin is raw bytes

**One-line rule:** argv is interpreted as the contents of a Go
double-quoted string literal via `strconv.Unquote`. Stdin and
`--input-file` are byte-verbatim.

Implementation is three lines:

```go
src := `"` + strings.Join(args, "") + `"`
out, err := strconv.Unquote(src)        // returns post-escape bytes, or parse error
```

We inherit Go's full string-literal grammar, validation, and error
messages for free. That gives users:

```
\xHH               one byte, two hex digits           (full 0x00-0xff range)
\uXXXX             Unicode codepoint, 4 hex           UTF-8 encoded
\UXXXXXXXX         Unicode codepoint, 8 hex           UTF-8 encoded
\NNN               one byte, exactly 3 octal digits   (value ≤ 0xff)
\a \b \f \n \r \t \v   standard letters
\\                 literal backslash
\"                 literal double-quote
\'                 literal single-quote
```

Everything else (`\q`, `\e`, `\<`, embedded literal newlines) is a parse
error from `strconv.Unquote` → exit 2 with Go's error message.

**Choice of source dictates the mode (no flag needed):**

| Source         | Interpreted?            | Use case                                 |
| -------------- | ----------------------- | ---------------------------------------- |
| Argv           | yes — Go string literal | human-typed input, scripts               |
| `--input-file` | no                      | pre-rendered byte stream, binary, paste  |
| Stdin pipe     | no                      | piping bytes from another tool, heredoc  |

This split is exactly how `printf` (interpret-argv) differs from `cat`
(verbatim-stdin). Picking the mode from the source removes the flag.

**Heredocs.** Multi-line input flows naturally through stdin:

```
hoot write sess --paste <<'EOF'
git diff --stat
git status
EOF
```

The `'EOF'` quoting prevents shell expansion; the bytes between the
markers go to the PTY verbatim. Multi-line via argv requires explicit
`\n` because Go string literals reject embedded literal newlines.

**Notable quirks of the Go grammar (call out in `--help`):**

- **No `\e`.** ESC is `\x1b`. Go's grammar doesn't define `\e` and we
  don't post-process. The cheat sheet documents this prominently.
- `\xHH` accepts the full byte range 0x00-0xff (unlike Rust, which
  restricts to 0x7F). Useful for arbitrary control bytes.
- `\u{…}` brace form (Rust/JS-modern) is **not** supported — Go uses
  fixed-width `\uXXXX` / `\UXXXXXXXX`. Users who want codepoints between
  4-hex and 8-hex widths zero-pad: `ÿ`, `\U0001F600`.
- Octal `\NNN` is exactly 3 digits in Go (e.g. `\033`, not `\33`).
  Almost no one writes octal in 2026; mentioning for completeness.

**Backtick raw on argv — punted.** `strconv.Unquote` also accepts Go raw
strings (`` `…` ``), and we could detect a leading backtick and switch
modes. Decided against for v1: stdin and `--input-file` already cover
the byte-verbatim case, and adding a parser-detected mode switch is
cognitive overhead for marginal gain. Easy to add later.

What you do NOT get (unchanged from earlier revisions):

- No named keys (`\<Up>`, `\<C-c>`, etc.). Cheat sheet of byte sequences
  in `--help` / README.
- No kitty keyboard protocol output.
- No timing directives.

### 4. Bracketed paste — `--paste` is on/off only; off by default; on errors if receiver isn't ready

Two modes:

- **off** (default): send body raw, do nothing.
- **on** (`--paste`): server checks `term.ModeGet(ModeBracketedPaste)`.
  - If true: wrap with `\e[200~ ... \e[201~` and write.
  - If false: **return 409, do not write anything**. CLI surfaces:
    `hoot write: --paste: bracketed paste not enabled on receiver` and
    exits 2.

No `auto` mode. Rationale: `auto` introduces silent behavior changes
between two `hoot write` calls seconds apart (the receiver's shell flips
2004 on when entering an editor, off when in raw `cat`). User asks for
`auto`, gets safe-paste once, gets execute-on-newline next time, and
swears at us. `--paste` is opt-in; if the receiver can't honor it, we
fail loudly so the caller knows.

The mode query must run on the dispatcher goroutine (the terminal is owned
by it — see `pty_libghostty.go:198`). Add a small accessor:

```go
func (p *LibghosttyPTY) BracketedPasteActive() bool {
    var v bool
    p.do(func() { v, _ = p.term.ModeGet(libghostty.ModeBracketedPaste) })
    return v
}
```

Edge cases:

- **Payload contains `\e[201~`**: refuse with HTTP 400 and a clear CLI
  error. The standard has no universally-accepted in-band escape;
  splitting around the marker would silently change semantics. Add a
  `--allow-paste-end-strip` if a real user ever asks (YAGNI for v1).
- **Receiver disables 2004 between the mode query and our write**: tiny
  TOCTOU window. Acceptable — the receiver flipping 2004 mid-flight is
  itself a race they're already living with from any attached client.
  Worst case: literal `\e[200~…\e[201~` bytes appear in the buffer. The
  user can re-issue without `--paste`.

### 5. Newline handling — send bytes verbatim, no rewriting

Recommendation: **post-encoding bytes go to the wire unchanged. No `\n` → `\r`
rewriting anywhere.**

- If the user writes `\r`, they get `\r`. If they write `\n`, they get `\n`.
- `\<Enter>` expands to `\r` (matches tmux's `Enter` token; matches what
  shells in raw mode treat as the Enter key).
- `--raw` mode is byte-faithful piping; same rule.

Document the gotcha in `--help`: "Most TUIs / shells run in raw mode and
treat `\r`, not `\n`, as the Enter key. If you pipe a file with LF endings
into `hoot write --raw`, the receiver sees LFs — that may or may not match
what you want. Use `\<Enter>` or `\r` in encoded mode to be explicit."

Precedents reinforcing this: `tmux send-keys -l` is byte-verbatim; kitty
`send-text` is byte-verbatim. Only `xdotool type` rewrites (it operates at
the X11 keysym layer — different problem).

### 6. CLI shape

```
hoot write [--remote URL] [--state-dir DIR]
           [--paste]
           [--input-file FILE]
           <session-prefix>
           [DATA...]
```

Behavior:

- `<session-prefix>` resolved via the same mechanism as `hoot kill` /
  `hoot detach` (full id or unique prefix; `hoot resolve` semantics).
- Body source — exactly one source, picked by precedence:
  - `--input-file FILE` — bytes from FILE, **verbatim, no escape parsing**.
  - `DATA...` positional — concatenate args (no separator), then
    **interpret C-style escapes** per §3.
  - else if stdin is not a TTY — bytes from stdin, **verbatim, no escape parsing**.
  - else: error, exit 2.
- `--paste`: see §4. Boolean, default off. The wrap happens server-side
  on the bytes that actually get sent (post-encoding for argv, raw for
  stdin/file). If the receiver does not have DECSET 2004 enabled, the
  server returns 409 and the CLI exits 2 with a clear error.
- Exit codes: 0 success; 2 usage / unknown session / parse error;
  3 transport error (dial / proxy); 4 server error (5xx).
- `--remote URL` parses the same as other subcommands (`parseRemoteFlag`
  in `cmd/hoot/remote.go:43`); when set, the request goes through
  `hoot serve` instead of the local rpc.sock.

Examples (these are what `--help` should illustrate):

```
# Type a command and run it.
hoot write sess "ls -l\r"

# Cancel a running command.
hoot write sess "\x03"

# Move down two lines, hit enter (TUI menu select).
hoot write sess "\x1b[B\x1b[B\r"

# Pipe a multi-line edit safely into a shell prompt (no auto-execute).
cat patch.diff | hoot write sess --paste

# Pipe a script via bracketed paste (errors if receiver isn't ready).
hoot write sess --paste --input-file script.sh

# Confirm a y/n prompt remotely.
hoot write --remote http://lab:8484 sess "y\r"

# Send raw bytes from a tool that already encoded them.
my-encoder | hoot write sess
```

Common byte cheat-sheet (lives in `--help` and the README):

```
Enter      \r           (most TUIs treat \r as Enter, not \n)
Tab        \t
Backspace  \x7f         (some apps want \b = \x08)
Escape     \x1b         (NOTE: no \e — Go grammar doesn't support it)
Ctrl-A..Z  \x01..\x1a   (Ctrl-C = \x03, Ctrl-D = \x04, ...)
Up         \x1b[A       Down  \x1b[B   Right \x1b[C   Left \x1b[D
Home       \x1b[H       End   \x1b[F
PageUp     \x1b[5~      PgDn  \x1b[6~
F1..F4     \x1bOP \x1bOQ \x1bOR \x1bOS
F5..F12    \x1b[15~ \x1b[17~ ... \x1b[24~
```

Multi-step flows (composed in shell, not built-in):

```
hoot write sess "/help\r"
sleep 0.2
hoot text sess | grep -q "Available commands" || exit 1
hoot write sess "submit\r"
```

### 7. Local + remote plumbing

Local (rpc.sock):

- Dial `<state-dir>/<key>/rpc.sock` with `dialUnixSock` (already exists,
  `cmd/hoot/attach.go:1174`).
- `POST http://unix/pty/input?paste=...&keys=...` with body.
- 204 → exit 0; 4xx → write server message to stderr, exit 2 (4xx) or 4 (5xx).

Remote (through `hoot serve`):

- Add a route on the serve mux: `POST /sessions/{key}/input` proxies to the
  upstream rpc.sock's `/pty/input`, mirroring the body and the `paste` /
  `keys` query params. Pattern is identical to `handleSignal` at
  `cmd/hoot/serve_spawn.go:186-233`. Add to the route table at
  `cmd/hoot/serve.go:60-71`.
- Document on the serve.go banner comment block.

## Architecture — files to touch

New:

- `cmd/hoot/write.go` — subcommand entry: flag parsing, body resolution
  (file / stdin / argv), encoding pass, local-vs-remote dispatch.
- `cmd/hoot/encode.go` — ~10-line wrapper around `strconv.Unquote`.
  Joins argv, surrounds with `"`, unquotes, returns `[]byte` or wraps the
  parse error with a friendlier prefix. No grammar of our own.
- `cmd/hoot/encode_test.go` — ~10 sanity cases (most behavior is owned
  by Go stdlib). Cover: escape passthrough, parse-error mapping to exit 2,
  multi-arg join, empty-argv guard, `\e` produces a clear "use \x1b" hint
  in the error message.
- `cmd/hoot/write_test.go` — local + remote happy paths, error paths
  (unknown session, paste-end in payload, parse failures, stdin-vs-argv
  mode pickup).

Modified:

- `cmd/hoot/main.go` — register the `write` dispatch, add usage line.
- `pty_libghostty.go`:
  - Add `writeMu sync.Mutex`.
  - Wrap `master.Write` in both writer sites (the `WithWritePty` callback
    and the exported `Write`).
  - Add `BracketedPasteActive() bool` accessor.
- `pty_libghostty_routes.go::handleInput`:
  - Parse `paste=on|off` query param (default `off`).
  - When `paste=on`: pre-validate body for `\e[201~` (400 if found),
    then call `BracketedPasteActive()`. If false, return 409 with the
    error message and do not write. If true, wrap and write.
  - 1 MiB body cap.
- `cmd/hoot/serve.go` + `cmd/hoot/serve_spawn.go`:
  - New `handleInput` proxy at `/sessions/{key}/input` mirroring `handleSignal`.
  - Pass through `?paste=…` query param.

Dependencies: none new. The encoding parser is a small handwritten state
machine plus a name table.

## Steps (becomes `## Todos`)

1. Land the spec; pause for human review at `rfc:` gate. **(this PR)**
2. Add `writeMu` and wrap both writer sites; tests for ordering under
   concurrent attach + WithWritePty + direct Write callers.
3. Add `BracketedPasteActive` accessor + unit test (drive a fake stream
   into the dispatcher, flip 2004, query, flip off, query).
4. Extend `handleInput` with `paste` query param, the 1 MiB cap, and the
   `\e[201~` rejection. Tests for each branch.
5. Implement `cmd/hoot/encode.go` — thin wrapper around `strconv.Unquote`
   for argv. Friendlier error wrapping. Sanity tests only (Go owns the
   grammar).
6. Implement `cmd/hoot/write.go`, register in main, write CLI tests
   covering argv (interpreted) vs stdin/file (verbatim) and `--paste`
   composition.
7. Add the `/sessions/{key}/input` proxy in serve. Bridge test + e2e against
   a real upstream supervisor.
8. README updates covering `hoot write` with 3 worked examples (encoded
   keys, `--paste --raw` for safe pastes, `--remote`).

## Verification (becomes `## Evidence`)

- Unit: `go test ./...` green; new tests cover the parser, the lock, the
  paste/keys query branches, and the proxy.
- E2E (local): start a hoot session running `bash`; `hoot write sess "echo hi\r"`;
  `hoot text sess` shows `hi` on screen. Capture both transcripts.
- E2E (paste happy path): start a session running `bash`; enable
  bracketed paste in bash (`bind 'set enable-bracketed-paste on'`);
  `cat multiline.sh | hoot write sess --paste`; assert text lands in
  readline buffer **without executing**. Then `hoot write sess "\r"` to
  execute. Transcript.
- E2E (paste error path): start a session running `cat` (no readline,
  no bracketed paste); `echo hi | hoot write sess --paste`; assert
  exit code 2 and stderr contains `bracketed paste not enabled`.
  Transcript.
- E2E (keys): `hoot write sess "\x03"` mid-`sleep 9999`; assert exit
  status surfaces. Transcript.
- E2E (remote): run `hoot serve` in the foreground, drive `hoot write
  --remote …`; same checks. Transcript.
- Concurrency: a stress test that fires 100 parallel `hoot write sess
  $LARGE` calls with distinct payloads; assert each payload appears
  contiguously in the recorded output (not interleaved). This is the
  proof the lock works.

## Open questions for the human

- **1 MiB body cap — too low / too high?** A 1 MiB shell paste is already
  pathological. If we want headroom for "paste a generated 5MB SQL dump",
  bump to 16 MiB. Defaulting to 1 MiB; please overrule if you want larger.
<!-- (paste default resolved in revision-5: on/off only, off by default, on errors if receiver not ready) -->

## Design notes

- 2026-05-07T(draft) — Picked extending `/pty/input` over a new endpoint or
  attach-like one-shot.
  - Alternatives:
    - **New `/input` route**: pure duplication. No.
    - **One-shot attach-like upgraded conn**: would buy streaming we do
      not need, and brings registry/recorder side-effects. Lost.
    - **Extend `/pty/input` (picked)**: route already exists, already
      tested, only needs query params + the lock that we wanted anyway.

- 2026-05-07T(draft) — Picked tmux send-keys vocabulary for `--keys`.
  - Losers:
    - **xdotool**: X11 layer, wrong abstraction. Lost.
    - **kitty CSI-u / keyboard protocol**: a wire encoding for terminals
      to talk *to* applications, not a CLI input grammar. Wrong layer.
    - **GNU readline `\C-a` form**: lives in config files, not CLI.
      Less widely typed by humans.
    - **No symbolic syntax at all (raw bytes only)**: was on the table per
      user's instinct ("don't invent"). Tmux already exists and is widely
      known, so adopting it isn't inventing. Net win for ergonomics.

- 2026-05-07T(revision-5) — Dropped `--paste=auto` entirely. `--paste`
  is on/off only; off is the default; on errors (409 → exit 2) when the
  receiver does not have DECSET 2004 enabled.
  - Human steer: "not a fan of paste auto. think just on or off. if can't
    paste (return an error)."
  - Right call. `auto` had two failure modes — silent fallback to raw
    bytes (which was the original concern about silent behavior changes
    between calls), and the fact that "polite to TUIs" is precisely the
    case where the caller already knows what it's doing. Forcing the
    caller to opt in and giving them a hard error if they're wrong is
    cleaner than guessing.
  - Server contract: `?paste=on` → check mode → wrap + write, OR 409.
    Never silently demote to raw.
  - Removed the "default off vs auto" open question from the list.

- 2026-05-07T(draft, superseded by revision-5) — Picked default
  `--paste=off`, not `--paste=auto`. (Kept here for history; the auto
  mode no longer exists.)

- 2026-05-07T(draft) — Picked "send bytes verbatim, no `\n`/`\r` rewriting".
  - Matches tmux `send-keys -l` and kitty `send-text`. The Enter case is
    handled explicitly via `--keys Enter` or by the user including `\r`.
  - Rejected: cooked-mode-style `\n` → `\r` rewrite. Too magic, breaks
    the principle that file-piping is byte-faithful.

- 2026-05-07T(revision-4) — Replaced our hand-rolled printf-style parser
  with `strconv.Unquote`. Argv is interpreted as the contents of a Go
  double-quoted string literal; stdin / `--input-file` stay verbatim.
  - Human steer: "can we just take the input as a golang multiline
    string, and interpret that for free using go?" — yes, and it's
    strictly better.
  - What we get for free: full Go string-literal grammar (`\xHH` full
    byte range, `\uXXXX`, `\UXXXXXXXX`, `\NNN`, letters), validation
    (surrogates, out-of-range Unicode, malformed escapes), sane error
    messages. Implementation collapses to ~10 lines.
  - What we lose:
    - `\e` — Go's grammar has no `\e`. Users write `\x1b`. Cheat sheet
      flags this prominently. The encode.go wrapper detects `\e` parse
      errors and adds a "(use \x1b for ESC)" hint.
    - Rust-style `\u{XXXX}` brace form — Go is fixed-width. Minor.
    - Embedded literal newlines in argv — not allowed in Go `"…"`.
      Heredoc/stdin is the multi-line path; argv uses `\n`.
  - Punted (could revisit later): supporting Go's backtick raw strings
    on argv (detect leading `` ` ``, switch parser mode). For v1 the
    stdin / `--input-file` path covers byte-verbatim adequately.
  - Open question about octal `\NNN` resolved by default — Go's
    grammar includes it, so we get it for free. Removed from open
    questions list.

- 2026-05-07T(revision-3) — Stripped to pure printf semantics. No named
  keys, no `\<…>` sugar, no `--raw` flag.
  - Human steer: "i don't really want to borrow the tmux vocab. i want
    write pretty much to be like printf. wysiwyg when you send a string."
  - Argv interpreted with C escapes; stdin and `--input-file` verbatim.
    Source picks the mode — exactly how `printf` (interpreting) and `cat`
    (verbatim) split today. No `--raw` flag needed.
  - Tradeoff: users who want Up arrow type `\x1b[A` instead of `\<Up>`.
    Mitigated by a cheat sheet in `--help` and README. The user
    explicitly chose the WYSIWYG-bytes ergonomic over the named-key
    ergonomic.
  - Why this is better than the previous `\<NAME>` revision:
    - Smaller parser (~50 lines vs ~150 with the name table).
    - No vocabulary lock-in to maintain forever.
    - The cheat sheet doubles as a teaching tool — users learn the actual
      VT byte sequences and stop being mystified by terminal escape
      codes elsewhere.
    - Matches `printf` muscle memory exactly. Zero new mental model.
  - Removed open question about vocabulary lock-in.

- 2026-05-07T(revision-2) — Replaced the `--keys` parse-mode flag with a
  single client-side encoding language (C escapes + `\<NAME>` named-key
  sugar). Default is interpret-escapes; `--raw` opts out for byte-faithful
  piping.
  - Subsequently superseded by revision-3 (above) — the `\<NAME>` sugar
    was dropped in favor of pure printf semantics.
  - What flipped my mind: human review pushed back on `--keys` as bolted-on.
    "We should have a principled way to encode the input string." Right
    call — two parse modes meant the user had to think about which mode
    they were in before composing input. One language always-on is
    simpler.
  - Alternatives considered:
    - **Keep `--keys` as separate parse mode**: original draft. Loser:
      mode flag is mental overhead; you can't mix literal text and named
      keys in one chunk without splitting argv awkwardly.
    - **Pure tmux tokens-as-args** (`hoot write sess "ls" Enter "C-c"`):
      Works for short bursts. Loser: shell quoting acrobatics for
      longer encoded chunks; can't mix with `--input-file` cleanly.
    - **Pure C escapes, no key sugar** (`\x1b[A` for Up): minimal, no
      lookup table. Loser: user has to remember byte sequences for every
      key; `\<F12>` vs `\x1b[24~` is a usability gulf.
    - **`\<NAME>` sugar (picked)**: one parser, unambiguous next to
      `\xHH`, multi-character names get a delimiter. Borrows tmux's
      *vocabulary* (well-known) inside our *surface* (encoded string).
  - Follow-on: server-side `?keys=…` query param gone. The server only
    ever sees post-encoding bytes. Removed parser duplication question
    from open questions list.

- 2026-05-07T(revision) — Dropped delay/pause primitives (`\p<dur>`,
  `--pause`, etc.) before they got into the spec.
  - Human steer: "the input, delay, input, observe/expect, input, read
    loop should actually be an outer program. let's just focus on having
    a good primitive for sending inputs."
  - Right call. `hoot write` becomes a unix-philosophy primitive: encode,
    send, exit. Multi-step orchestration composes via shell:
    `hoot write … && sleep 0.2 && hoot text … | grep && hoot write …`.
  - Alternatives the steer ruled out:
    - **In-band `\p200ms` directive**: would have meant the encoding
      language is no longer pure-bytes; client splits into chunks; one
      logical "write" becomes many round trips. Scope creep.
    - **Outer-loop expect-style waits**: even bigger scope.
  - This kept the protocol surface simple too — no need for a streaming
    mode or chunked-write semantics on `/pty/input`.

- 2026-05-07T(draft) — Decided to lock `master.Write` despite the existing
  comment that says it's safe.
  - The comment is accurate at the kernel layer for writes ≤ PIPE_BUF
    (~512B macOS). Fine for attach single-keypress traffic.
  - `hoot write` introduces multi-KB payloads as a normal case. Two
    such writes interleaving would produce a corrupted shell line.
    User explicitly steered: lock.
  - Trade-off: the lock is held during a syscall on a possibly-blocking
    fd. If the consumer stalls (won't drain `master.Read`), writers block.
    Not new — the same is true today for any large attach paste — but
    flagged as follow-up: short-write loop with a deadline.
