# spec — `hoot attach` fuzzy picker (via fzf)

## Goal

When `hoot attach` is run without an id, shell out to `fzf` over the
sessions on the targeted host (local or `--remote`) and attach to the
chosen one. Also extend `hoot attach <pattern>`: try the literal as an
id first, and on no-match shell out to fzf with `--query=<pattern>
--select-1 --exit-0` so a unique fuzzy hit auto-attaches, zero hits
exit cleanly, and multiple drops into the picker pre-seeded.

Out of scope: server-side picker, scoring/ranked-match config,
configuration of the line format, persistence of "last picked",
fallback for hosts without fzf installed.

## Recommendation summary

1. **Matcher / UI**: delegate to the `fzf` binary on PATH. No own
   TUI, no vendored matcher. fzf does the rendering, key handling,
   and (via `--select-1 --exit-0`) the unique-hit fast path.
2. **Line format**: leading numeric index (display only — hidden from
   the matcher) followed by `[id] @host  cwd  cmd args  *attached`.
   Single-char markers `@`, `[`, `*` give natural prefix-scoping in
   fzf queries; the cmd field is unmarked.
3. **Resolver**: thin `internal/sessionpick` package — just `Format`
   (one line per session) and `IDResolve` (id-prefix only). The fuzzy
   half lives entirely inside fzf.
4. **CLI**: no-args + TTY → fzf picker; no-args + non-TTY → error;
   `<pattern>` → id-prefix first, then `fzf --query=<pattern>
   --select-1 --exit-0`; `--strict` skips fzf entirely.
5. **fzf required**: hard runtime dep. If fzf is not on PATH the
   command errors with a one-line install hint and exits 2. No
   fallback mini-picker.

## Architecture

### Files to add / touch

- `internal/sessionpick/sessionpick.go` (NEW)
  - `type SessionWithMeta struct { State session.StateFile; Alive bool; Host string }`
  - `Format(s SessionWithMeta) string` — one rendered line, no leading
    index (the index is added at picker-feed time so it stays in sync
    with the post-filter view fzf controls).
  - `IDResolve(states []SessionWithMeta, arg string) (SessionWithMeta, error)`
    — current `session.Store.Resolve`-style id-prefix resolution,
    surfaced as a pure-data function so it works for both local and
    remote-fetched lists. Returns `ErrNoMatch` / `ErrAmbiguous`.
- `internal/sessionpick/sessionpick_test.go` (NEW) — table tests for
  Format and IDResolve.
- `cmd/hoot/attach_picker.go` (NEW) — `runFZFPicker(states, query) (key string, err error)`.
  - Looks up `fzf` on PATH; on miss returns a sentinel error mapped
    to exit 2 with install hint.
  - Builds the input lines (with leading `N\t` index column).
  - Spawns fzf with the flag set described below.
  - Parses the picked line, returns the session key.
- `cmd/hoot/attach_picker_test.go` (NEW) — tests against a fake
  `fzf` shim on PATH (a tiny shell script in `t.TempDir()` prepended
  to PATH) so we can verify argv, stdin lines, and stdout parsing
  without depending on the real fzf during `go test`.
- `cmd/hoot/attach.go` — relax `len(rest) != 1` to support 0-or-1.
  - 0 args + TTY → fetch sessions, run fzf picker (empty query),
    attach to picked key
  - 0 args + non-TTY → error, exit 2
  - 1 arg → `IDResolve` first; on unique hit attach (today's
    behavior). On `ErrNoMatch` and not `--strict` → run fzf picker
    with `--query=<arg> --select-1 --exit-0`. On `ErrAmbiguous`
    surface the existing error (id beats fuzzy).
  - `--strict` → `IDResolve` only, never invoke fzf, never picker.

### fzf invocation

```go
args := []string{
    "--ansi",
    "--no-sort",                       // we pre-sort by CreatedAt asc
    "--delimiter=\t",
    "--with-nth=2..",                  // hide leading N column from matcher
    "--prompt=attach> ",
    "--header=↑↓ select · enter attach · esc cancel",
    "--height=40%",
    "--reverse",
    "--no-mouse",
}
if preseed != "" {
    args = append(args, "--query="+preseed, "--select-1", "--exit-0")
}
cmd := exec.Command("fzf", args...)
cmd.Stdin = linesReader            // tab-separated, one per session
cmd.Stdout = &picked
cmd.Stderr = os.Stderr
err := cmd.Run()
```

`fzf` opens `/dev/tty` itself for keyboard + display, so it composes
cleanly with our pipes.

### Input line shape

One tab-separated record per session, written to fzf's stdin. The
leading `N` column is purely visual; `--with-nth=2..` excludes it
from the matcher.

```
1\t[a3f]\t@m4mini\t~/proj/api\tbash --login\t*attached
2\t[k7q]\t@local\t~/Dropbox/notes\tnvim spec.md
3\t[zz9]\t@local\t~/code/hootty\tgo test ./...\t(idle 12m)
```

Fields, fixed order:

- `N` (field 1, display-only): 1-based index in our pre-sorted list.
- `[xxx]` (field 2): short id. Marker `[` gives `[a3` id-prefix
  queries.
- `@host` (field 3): `@local` or `@<remote-display>`. Marker `@`
  gives host-prefix queries.
- `cwd` (field 4): tildified `~/...` path.
- `cmd args` (field 5): argv joined with spaces.
- Trailing tag (field 6): `*attached` if any attachments, else
  `(idle <age>)`. Marker `*` gives "live only" queries.

Two-space visual separation between groups isn't needed — fzf renders
tab-separated fields with its own column alignment via `--delimiter`.

### fzf query vocabulary (free, from fzf itself)

These all come straight from fzf's extended-search syntax and need no
code on our side:

- `m4mini` — fuzzy substring
- `'nvim` — exact-match (fzf flips fuzzy off for the term)
- `^[a3f]` — line prefix (matches the leading `[id]` since field 1
  is hidden)
- `m4$` — line suffix
- `!notes` — negate
- `nvim | vim` — OR
- spaces between terms — implicit AND
- `--nth=3` (runtime) — scope to host field; etc.

We document the markers (`@`, `[`, `*`) in `hoot attach -h` so users
know what natural prefixes work; fzf does everything else.

### Ordering and selection

We pre-sort by `CreatedAt` ascending and pass `--no-sort` so fzf
preserves that order. fzf then re-orders by its own match score
during typing, but `--no-sort` keeps the empty-query view stable
(oldest first, "I just want my long-lived session"). The leading `N`
index reflects this pre-sorted order; once fzf re-ranks during
filtering, the indices on screen are no longer 1..n contiguous —
that's a known cosmetic quirk we accept (see Open question 2).

Selection is fzf-native: Up/Down, Enter to attach, Esc/Ctrl-C to
cancel.

### Resolver behavior (id-prefix only, lives in `internal/sessionpick`)

```go
func IDResolve(states []SessionWithMeta, arg string) (SessionWithMeta, error)
```

Algorithm:

1. Match `arg` as a unique session-key prefix across `states`. Unique
   hit → return it. Multiple hits → `ErrAmbiguous` with candidates.
   Zero hits → `ErrNoMatch`.

That's it. The fuzzy half is `fzf --query=<arg> --select-1 --exit-0`
and lives in the caller (`cmd/hoot/attach.go`), not here.

The "id beats fuzzy on collision" rule is enforced at the call site:
on `ErrAmbiguous` we surface the error and do **not** fall through to
fzf; on `ErrNoMatch` we do fall through.

### Remote picker

Picker runs **client-side** in all cases (fzf only needs to be on the
local machine):

- `hoot attach` (no args, no `--remote`) — local: `session.Store.List()`
- `hoot attach --remote …` — `GET /sessions` (existing endpoint;
  returns `[]{StateFile, Alive bool}` — confirmed in `cmdList` and
  `serve.go:61`). The host shown in the line is `remote.display` for
  every row.

No server changes.

### CLI shape & non-TTY behavior

| invocation                        | tty? | behavior                                                            |
|-----------------------------------|------|---------------------------------------------------------------------|
| `hoot attach`                     | yes  | fzf picker over all sessions                                        |
| `hoot attach`                     | no   | error: "no session id given; tty required for picker", exit 2       |
| `hoot attach <id-prefix>`         | -    | unique id-prefix → attach (today's behavior)                        |
| `hoot attach <pattern>` (no id)   | yes  | `fzf --query=<pattern> --select-1 --exit-0`; auto-attach on unique  |
| `hoot attach <pattern>` (no id)   | no   | error, exit 2 (same: tty required for picker)                       |
| `hoot attach --strict <arg>`      | -    | id-prefix only, no fzf invocation                                   |
| any of the above, fzf not on PATH | -    | error: "fzf not found on PATH; install fzf or pass --strict <id>"   |

`--select-1` makes the unique-hit case a no-flicker auto-attach
(fzf opens, sees one match, exits 0 without drawing). `--exit-0`
makes the zero-hit case exit cleanly with code 1, which we map to
"no match" exit 2.

### fzf exit code mapping

| fzf exit | meaning             | hootty exit |
|----------|---------------------|-------------|
| 0        | line selected       | 0 (proceed) |
| 1        | no match (--exit-0) | 2 (no match)|
| 2        | error               | 1           |
| 130      | Ctrl-C / Esc        | 130         |

### Why the hard fzf dep is acceptable

- fzf is one `brew install fzf` / `apt install fzf` on every platform
  hootty already runs on; users overwhelmingly already have it.
- The picker is opt-in: `hoot attach <id>` and `hoot attach --strict`
  never invoke fzf. Scripted users are unaffected.
- The error message tells them how to fix it.

If we ever want to lift the dep, the line format and resolver are the
reusable pieces — a future built-in mini-picker (or a re-vendoring of
`fzfmatch`) plugs in below `runFZFPicker` without touching the
caller.

## Steps (becomes Todos)

1. Write `internal/sessionpick/` — `SessionWithMeta`, `Format`,
   `IDResolve`, sentinels, table tests.
2. Implement `cmd/hoot/attach_picker.go` — `runFZFPicker(states,
   preseed)`: PATH check, argv assembly, stdin feed, stdout parse,
   exit-code mapping.
3. Wire 0-args path in `cmd/hoot/attach.go` (TTY → picker, non-TTY
   → exit 2). Add `--strict` flag.
4. Wire 1-arg path: `IDResolve`, then on `ErrNoMatch` (and not
   `--strict`) call `runFZFPicker(..., preseed=arg)`.
5. Picker tests via fake `fzf` shim on PATH (shell script that
   echoes its argv to a file and prints a chosen line).
6. Update `hoot attach -h` to document the line format, fzf
   requirement, and `--strict`. Update README §attach.
7. End-to-end smoke: spawn 3 sessions, run `hoot attach`, type a
   filter, pick row 2, confirm correct session attaches. Repeat
   with `hoot attach nvim` (unique → auto-attach) and `hoot attach
   --remote <host>`. Capture transcript for `## Evidence`.

## Verification

- `go test ./internal/sessionpick/... ./cmd/hoot/...` passes.
- Picker tests use a fake `fzf` shim and don't require the real
  binary to be installed in CI.
- Manual transcript:
  - three sessions in different cwds / cmds, `hoot attach` no args,
    type partial cwd, Enter → attaches
  - `hoot attach nvim` with one nvim session → direct attach (no UI)
  - `hoot attach nvim` with two → picker shows both, pre-filtered
  - `hoot attach a3` (real id prefix) — id wins, no fzf invocation
  - `hoot attach </dev/null >/dev/null` — exit 2
  - `hoot attach --remote <ssh-host>` (no args) — picker over remote
    list
  - `PATH=/usr/bin hoot attach` — error mentioning fzf install

## Open questions

1. **Exit code on Esc**: fzf returns 130 for both Ctrl-C and Esc.
   We pass it through unchanged. OK?
2. **Leading `N` index re-ranking**: once the user types a query, fzf
   re-ranks by score and the visible indices are no longer contiguous
   1..n. Acceptable cosmetic quirk, since the user picks via Up/Down
   anyway. Alternative: drop the `N` column entirely (fzf already
   shows a `N/M` counter at the bottom). Vote?
3. **`*attached` vs `(idle <age>)` trailing field**: simple/MVP, but
   adds noise. Could split into two fields. Defer to a later iter
   unless you want it now.

## Design notes

- 2026-05-07T05:05Z — Three picker UX simplifications, all from human review.
  - **Leading `N` index column added.** Reassigned every filter pass.
    Pure display: matcher sees the line starting at `[id]`, so users
    can't accidentally type a query that filters by row number.
    Bumped from "post-MVP nicety" because it's also the natural
    answer to "how many results does my query match" without adding
    a separate counter line.
  - **`$` cmd marker removed.** Two reasons. (1) `$` is fzfmatch's
    right-anchor operator (`foo$`), so a leading `$` token would be
    confusing-at-best and might surprise users who reach for it as
    an end-anchor in a future query. (2) fzfmatch's word-boundary
    forms (`'nvim'`, `'nvim`) already give clean cmd-token matching
    without needing a marker char to disambiguate. The cmd field is
    just unmarked text now.
  - **`:N<Enter>` row-jump dropped.** Originally proposed as a
    keyboard-fast pick, but human's call: fzf-style + Up/Down is
    enough; no vim-style modal escapes wanted. Selection is now
    purely cursor-driven.

- 2026-05-07T05:25Z — **Pivot: delegate to fzf binary instead of
  vendoring `fzfmatch` and writing our own TUI.** Human's call after
  weighing the integration sketch.
  - What flipped: the integration cost of own-impl was ~250 LoC of
    raw-mode TUI + scripted-FSM tests + a vendored matcher with
    sync-drift risk. The fzf-delegation path replaces all of that
    with one `exec.Command` and a tab-separated stdin feed. fzf's
    `--select-1 --exit-0` also subsumes most of the resolver's
    "fuzzy with disambiguation" logic for free.
  - Alternatives reconsidered:
    - **Own impl with vendored fzfmatch** (prior recommendation):
      zero runtime deps, full control over line shape and key
      bindings. Lost on code volume + testability — fzf has been
      battle-tested for years and the TUI rewrite costs more than
      the dep is worth.
    - **Hybrid (detect fzf, fall back to mini-picker)**: explicitly
      rejected. Two code paths to maintain, tests for both, error
      messages get muddier ("which picker am I in?"). The cost of
      assuming fzf is installed is one error message; the cost of
      hybrid is permanent.
    - **fzf delegation (picked)**: hard dep, simpler everything
      else. Picker only runs on the local machine even with
      `--remote`, so the dep doesn't propagate to servers.
  - Follow-on consequences:
    - `internal/fzfmatch/` vendoring step deleted from the plan.
    - `internal/sessionpick/` shrinks to `Format` + `IDResolve` —
      no in-process `Filter` needed.
    - `attach_picker.go` becomes `exec.Command` glue, ~80 LoC
      instead of a raw-mode FSM.
    - Tests pivot to a fake-fzf shim on `PATH` for hermetic CI runs.
    - The leading `N` index becomes cosmetic-only once fzf re-ranks
      by score (Open question 2 captures this).
    - Field markers (`@`, `[`, `*`) survive unchanged — they're
      generic prefixes that work with fzf's extended-search syntax
      just as well as fzfmatch's. The `$` removal still applies
      (fzf also uses `foo$` as a tail anchor).
  - Hidden risk: fzf version skew. Some flags (`--with-nth`,
    `--select-1`, `--exit-0`, `--no-sort`, `--ansi`) are stable
    across all currently-shipping fzf versions (≥0.20). We don't
    use any cutting-edge flags. If a user has a truly ancient fzf
    we accept the breakage; the install hint covers them.
