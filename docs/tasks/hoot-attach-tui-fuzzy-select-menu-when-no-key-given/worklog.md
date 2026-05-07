---
status: done
section: Hoot attach — TUI fuzzy select / menu when no key given
slug: hoot-attach-tui-fuzzy-select-menu-when-no-key-given
mode: worktree
spec: spec.md
created: 2026-05-07T04:41:51Z
---

> ## Hoot attach — TUI fuzzy select / menu when no key given
>
> ---
> status:
>   type: open
> ---
>
> Discussion / spec mode. Spawn a claude agent. No code yet.
>
> ### Goal
>
> When `hoot attach` is run without a session key, drop the user into a quick fuzzy/menu picker over the available sessions instead of erroring out. The user is unsure of the right approach and wants a real design discussion.
>
> ### Open design questions for `spec.md`
>
> - **Own implementation vs delegate to fzf vs both.** The user has both options on the table. The repo at `~/github.com/hayeah/dotfiles/libs/hayeah-go/fzfmatch/fzfmatch.go` is a Go fzf-style fuzzy matcher (vendor candidate). `fzf` itself is a battle-tested CLI binary. Survey:
>   - **Own (vendor `fzfmatch`)**: zero external dependency at runtime, works headless / over `hoot serve` if we ever wanted that, full control over the line shape and resolver. Costs: implementing the TUI loop (raw mode, line redraw, key handling), adding ~few hundred LOC.
>   - **Delegate to `fzf` binary**: zero TUI work; users already have fzf installed; consistent UX with their other tooling. Costs: hard runtime dependency on fzf being on PATH; harder to compose programmatically; less control over line shape.
>   - **Hybrid**: detect fzf on PATH and use it; fall back to a built-in mini-picker. The mini-picker can be very simple (no fuzzy match — just numbered list + enter) since fzf covers the power-user case.
>   - The user's framing: "we don't have THAT many sessions, so crazy performance is superfluous". So a vendored Go matcher is plenty. But fzf gives users a familiar UX. Push back / pick.
> - **Line design.** This may be the most interesting axis. The user wants the line for each session to encode multiple matchable fields with explicit markers, so fzf-style queries can be field-scoped. Sketch concrete lines and how a query maps to fields. Examples to think through:
>   - `[a3f] host=m4mini cwd=~/proj/api cmd=bash --login arg=`
>   - `[a3f] @m4mini ~/proj/api $ bash --login`
>   - Pick a syntax that's both readable as a single line AND lets fzf match cleanly. fzf supports field-extraction with `--nth` and exact / negation operators (`'foo`, `!bar`, `^foo`, `bar$`). Use that vocabulary if delegating; design something analogous if owning.
>   - Specifically address: by host, by cwd, by cmd, by arg, by session-id (the prefix in `[..]`), by attached/idle status.
> - **Stable numeric ID for filtered results**, sorted by spawn time ascending (oldest first). Goal: after typing a query, the user sees `1) ... 2) ... 3) ...` and can hit `1` `Enter` to attach without arrow keys. Question: do we let fzf produce that ordering (it has `--with-nth` and stable result ordering), or build it ourselves? In the own-implementation case the Go matcher returns ranked matches; we re-sort by spawn time after match-filtering — clarify whether the rank or the spawn time wins.
> - **Extending `hoot attach <pattern>`** with fuzzy fallback. The user proposed: `hoot attach "host cwd arg"` should first try the literal as an id (current behavior), then if that fails fall back to fuzzy-matching against the line vocabulary above. If exactly one match: attach. If multiple: drop into the picker pre-seeded with the query. If zero: error. Confirm or push back. Edge case: a literal id prefix that happens to also fuzzy-match other sessions — id wins because that's the lower-surprise rule.
> - **Resolver structure.** Both paths (interactive picker and `attach <pattern>`) need the same line-format + match-against-line logic. Pull that into a shared package; the picker UI and the CLI fallback both use it.
> - **Vendoring policy.** `fzfmatch` lives in dotfiles. Vendor the matcher into hootty (`internal/fzfmatch/`?) or use Go modules + a `replace` directive? Vendor is simpler for a lib that's not yet stable; Go-module `replace` matches the existing `dotfiles` pattern hootty already has if any. Check `go.mod` for prior art.
> - **Remote sessions.** `hoot attach --remote …` over `hoot serve` — does the picker run client-side after fetching the remote session list, or server-side? Client-side is simpler and consistent with how the local picker works. Confirm the remote `GET /sessions` returns enough metadata for line construction.
> - **CLI shape & non-TTY behavior.**
>   - `hoot attach` (no args) and stdin/stdout is a TTY → interactive picker.
>   - `hoot attach` with no args and stdout NOT a TTY → error (no sense in a picker without a screen) OR list-and-exit (`like fzf -f`)? Pick.
>   - `hoot attach <pattern>` → id-then-fuzzy-fallback.
>   - `--no-picker` / `--strict` flag to disable the fallback if scripts want hard-id semantics.
>
> ### Recommendation
>
> Pick: built-in vs fzf-delegation vs hybrid. Define the line format. Define the field-match vocabulary. Define the resolver (used by both picker and `<pattern>`). Sketch the implementation surface (files to touch, vendoring approach).
>
> ### Pre-plant rfc gate
>
> Don't write code. Produce `spec.md` answering the above with a recommendation. The human reviews before any implementation.
>
> - [ ] rfc: review spec.md (line format + matcher choice + resolver)
> - [ ] implement per spec

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### rfc gate (current)

- [x] survey hootty `attach`, `list`, `state.go`, `go.mod`; survey dotfiles `fzfmatch`
- [x] write spec.md with recommendation (own impl + vendored fzfmatch, line format, resolver, CLI shape)
- [x] await human review of spec.md (boss ticks `rfc: review spec.md` when satisfied)

### implement (after rfc tick) — fzf-delegation route

- [x] new `internal/sessionpick/` — `SessionWithMeta`, `Format`, `IDResolve` + table tests
- [x] new `cmd/hoot/attach_picker.go` — `runFZFPicker(states, preseed)`: PATH check, argv, stdin feed, stdout parse, exit-code mapping
- [x] wire 0-args path in `cmd/hoot/attach.go` (TTY → picker, non-TTY → exit 2); add `--strict` flag
- [x] wire 1-arg path: `IDResolve` → on `ErrNoMatch` (and not `--strict`) call `runFZFPicker(..., preseed=arg)`; id-ambiguity wins over fuzzy
- [x] picker tests via fake `fzf` shim on PATH (hermetic, no real fzf needed in CI)
- [x] update `hoot attach -h` (line format, fzf required, `--strict`) and README §attach
- [x] e2e smoke transcript → `## Evidence`

## Agent log

- 2026-05-07T06:25Z — README updated (8d50250) with a Picker subsection covering argv shapes, line format, field-marker query vocabulary, sort order, `--strict`, fzf install requirement, and client-side-only remote behavior.
- 2026-05-07T06:35Z — E2E smoke against real fzf 0.57.0: spawned three sessions (`cv9`, `ysw`, `dbp` with `s3-nvim` cmd), exercised the routing matrix. `hoot attach nvim` via `expect`-allocated pty triggered the fuzzy-fallback path: fzf got `--query=nvim --select-1 --exit-0`, saw the unique match, auto-attached as `dbp`, prefix-period detached cleanly. Transcript at `tmp/picker.log`.
- 2026-05-07T06:40Z — Section complete. All implement-phase todos ticked; evidence and trouble report populated; status → done.
- 2026-05-07T06:10Z — Wired fzf picker into `cmd/hoot/attach.go` (3019b94). Routing matrix from spec.md fully implemented; `--strict` preserves the prior single-arg semantics for scripts; unified `loadSessionList` handles local + remote cases. `go test ./...` green; previously passing tests untouched. Updated `-h` text with the picker line format and fzf query vocabulary so users discover the field markers without reading the spec.
- 2026-05-07T05:55Z — Landed `runFZFPicker` (f1d9ff3). Hermetic tests use a `/bin/sh` shim installed on `PATH` from `t.TempDir()` so CI doesn't need real fzf — covers argv assertions, stdin shape, preseed → `--select-1 --exit-0`, and the three exit-code branches (0/1/130) plus the missing-binary path.
- 2026-05-07T05:45Z — Landed `internal/sessionpick` (972dfba). `Format` renders tab-separated lines `[id]\t@host\tcwd\tcmd args\ttag` (tag = `*attached` / `(dead)` / empty). `IDResolve` wraps the existing `shortid.Resolve` over a pre-loaded list so the local + remote picker paths share one resolver. Six format cases + five resolver cases pass.
- 2026-05-07T04:50Z — Set up worktree at `~/github.com/hayeah/hootty/.worktrees/<slug>` and symlinked it under `repos/`. Read `attach.go`, `list.go`, `state.go`, `go.mod`, and the dotfiles `fzfmatch.go` to ground the spec.
- 2026-05-07T04:55Z — Drafted `spec.md` recommending: own implementation, vendored `fzfmatch` (copy, not module-replace, since hootty has no existing dotfiles dep), single-line format `[id] @host  cwd  $cmd  *attached` with single-char field markers that double as fzfmatch-friendly query prefixes, shared `internal/sessionpick` resolver used by both picker and `<pattern>` fallback, `--strict` flag for hard id semantics, exit 2 on no-tty + no-args. Filtered results re-sorted by `CreatedAt` ascending; `:N<Enter>` jumps to filtered row N.
- 2026-05-07T04:55Z — Three open questions parked at the bottom of `spec.md`: cancel exit code (130 vs 1), whether to surface size/last-activity in the line, and how to truncate long argv. Setting `status: blocked` per the pre-planted `rfc: review spec.md` gate.
- 2026-05-07T05:15Z — Human revisions to the picker UX (added leading `N` index column, dropped `$` cmd marker, dropped `:N` row-jump). Spec + design notes updated.
- 2026-05-07T05:30Z — **Pivot to fzf-delegation route** after sketching the integration. Rewrote spec body: `internal/fzfmatch/` vendoring removed; `internal/sessionpick/` reduced to `Format` + `IDResolve`; `cmd/hoot/attach_picker.go` becomes ~80 LoC of `exec.Command` glue; tests via fake-fzf PATH shim. fzf becomes a hard runtime dep with a one-line install hint on miss. Design note appended capturing alternatives and consequences. Still `blocked` on the rfc gate.

## Boss log
- 2026-05-07T05:29Z ticked: rfc

## Evidence

### Unit tests

```
$ go test ./... 2>&1 | tail
ok      github.com/hayeah/hootty                7.641s
ok      github.com/hayeah/hootty/cmd/hoot       5.278s
?       github.com/hayeah/hootty/internal/attachetest   [no test files]
?       github.com/hayeah/hootty/internal/attachwire    [no test files]
ok      github.com/hayeah/hootty/internal/sessionpick   (cached)
ok      github.com/hayeah/hootty/internal/shortid       0.896s
ok      github.com/hayeah/hootty/internal/sshtransport  1.549s
```

New tests:

- `internal/sessionpick/sessionpick_test.go` — 6 Format cases
  (alive/idle/dead/remote-host/empty-host/HOME-itself) + 5 IDResolve
  cases (unique / ambiguous / no-match / too-short / exact-full-key).
- `cmd/hoot/attach_picker_test.go` — 6 picker cases via `/bin/sh` shim
  on PATH: argv flags, stdin shape, preseed adds `--query --select-1
  --exit-0`, exit 130 → cancelled, exit 1 → no-match, missing binary
  → `errFZFNotFound`. CI doesn't need real fzf.

### CLI smoke (real binary, real fzf 0.57.0)

Three sessions spawned:

```
$ hoot list --state-dir tmp/state
{"session":{"key":"cv9", ..., "argv":["/bin/sh","-c","echo s1; sleep 600"], ...}}
{"session":{"key":"ysw", ..., "argv":["/bin/sh","-c","echo s2; sleep 600"], ...}}
{"session":{"key":"dbp", ..., "argv":["/bin/sh","-c","echo s3-nvim; sleep 600"], ...}}
```

Routing matrix exercised:

- `hoot attach </dev/null` (no args, non-tty) → exit 2,
  `hoot attach: no session id given; tty required for picker` ✓
- `hoot attach --strict cv9` → attached as `cv9`, prefix-period
  detached cleanly ✓
- `hoot attach --strict c` → exit 2, `query "c" too short
  (minimum 3 characters)` ✓
- `hoot attach nvim </dev/null` (pattern, non-tty) → exit 2,
  `no session matched "nvim" (tty required for picker)` ✓
- `hoot attach nvim` (pattern, tty, via `expect`-allocated pty) → fzf
  with `--select-1 --exit-0` saw a unique fuzzy match (`s3-nvim`) and
  auto-attached without UI flash; transcript at `tmp/picker.log`:

  ```
  spawn /tmp/hoot-test attach --state-dir tmp/state nvim
  [connected. dbp @ local]
  ...
  [disconnected. dbp @ local]
  ```
  ✓

The interactive (no-args) picker UI was not transcripted — fzf opens
`/dev/tty` itself and renders frames that don't survive a logfile
capture cleanly. The fzf invocation is identical to the preseeded
case above except for the absence of `--query/--select-1/--exit-0`,
and the unit tests cover argv assertions for both code paths.

## Trouble report

- Hootty's `go.mod` has no replace directives and no existing dotfiles dep, so module-replace vendoring would have been net-new infra. Mid-design we pivoted to fzf delegation, which made this moot — no vendoring needed.
- `SessionState.CreatedAt` exists, `Argv`/`CWD` exist, but **host** is not stored on the StateFile — `Origin.Host` is per-attachment only. Format sidesteps by showing `@local` for local sessions and `@<remote-display>` for remote — no schema changes needed, but worth noting if a future "show originating host" feature comes up.
- The top-level `hoot` synopsis (in `cmd/hoot/main.go`) still shows the old `hoot attach <id-or-prefix>` form; only the per-subcommand `--help` was updated. Out of scope for this section but flagging it — should be updated alongside the README in a small follow-up.
- The interactive picker UI couldn't be transcripted via `script` / `expect` because fzf renders to `/dev/tty` directly and the alt-screen/control sequences don't replay meaningfully in a log file. The auto-attach via `--select-1 --exit-0` was end-to-end verified, and the unit tests assert the argv we pass to fzf in both code paths, so the visible interactive UX is fzf's responsibility verbatim.
- Behavior change for `--remote` non-strict: the old code passed the user-typed arg straight to the server which resolved id-prefix server-side. The new non-strict path always client-side resolves first via `IDResolve`; the server still resolves for `--strict`. Net effect: identical for valid id-prefixes; better error messages for typos (we know the candidate list locally) at the cost of one extra `GET /sessions` round-trip on each remote attach without `--strict`.

