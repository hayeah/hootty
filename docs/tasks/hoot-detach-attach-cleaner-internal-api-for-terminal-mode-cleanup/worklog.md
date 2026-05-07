---
status: done
section: Hoot detach/attach — cleaner internal API for terminal-mode cleanup
slug: hoot-detach-attach-cleaner-internal-api-for-terminal-mode-cleanup
mode: worktree
spec: spec.md
created: 2026-05-07T06:07:08Z
---

> ## Hoot detach/attach — cleaner internal API for terminal-mode cleanup
>
> ---
> status:
>   type: open
> ---
>
> Spec mode. Spawn a claude agent. No code yet — this is a design pass.
>
> ### Context
>
> `4t6` (codex) wrote a spec under `ghostty-progress-indicator-stuck-on-after-hoot-detach-research/spec.md` diagnosing the Ghostty progress-bar-stuck-after-detach bug and proposing a one-sequence fix (add `OSC 9;4;0` to `cmd/hoot/attach.go::resetTermModes`). That spec is **already in your workspace as `spec.md`** — read it end-to-end before anything else. It's a good diagnosis and a small fix; don't redo the diagnosis.
>
> The human's concern: `cmd/hoot/attach.go` has accumulated a pile of terminal-mode cleanup logic — `resetTermModes`, `terminalModeTracker`, `seqXxx` byte constants, conditional kitty-keyboard pop, modify-other-keys off, and now an OSC 9;4 clear on top. The shape is a bag of loose helpers. As we keep adding modes (the codex spec's "Future Work" lists more: app cursor keys, app keypad, OSC 104/110/111/112 palette/color resets), this is going to get messier.
>
> The human's hunch: the constructor for an attach session should *receive* the things it needs to clean up at detach time, instead of `cmd/hoot/attach.go` doing it as a post-hoc bag of byte-string emits. Maybe `terminalModeTracker` becomes the one place where modes get registered (with their entry/exit sequences) and the attach lifecycle drives entry-on-attach + exit-on-detach. Or maybe a different structure entirely. Push back and explore.
>
> ### Goal
>
> Take the current `cmd/hoot/attach.go` cleanup machinery and propose a cleaner internal API. The OSC 9;4 fix from the codex spec should land naturally as a side-effect of the cleaner design — not as an additional `seqXxx` byte string slotted into an ever-growing list.
>
> ### Read first
>
> In order:
>
> 1. `spec.md` already in this workspace (codex's diagnosis + recommendation).
> 2. Source: `cmd/hoot/attach.go` — pay attention to:
>    - `runAttachLoop`, `runConnectLoop`, `runSession` (the lifecycle)
>    - `terminalModeTracker` (current registry of observed modes)
>    - `resetTermModes` (the bag of byte emits)
>    - `emitDetach` (where reset happens)
> 3. Source: `cmd/hoot/attach_term_modes_test.go` — the existing tests document the order/shape we have to preserve.
> 4. The kitty-keyboard pop work (`237996e` mentioned in codex's spec) — it's the closest example of a per-mode lifecycle. Walk through how it tracks the pop count.
>
> ### Open design questions for `spec.md` (you'll rewrite spec.md; codex's text becomes a baseline you can quote/replace)
>
> - **Do we have a "TerminalMode" abstraction worth lifting out?** Each mode has: a name, a way to detect that it was set (either by sniffing output bytes the inner program sends, or by client-side counting like kitty kbd push/pop), an entry sequence (sometimes), and an exit sequence (always). Is that an interface? A struct with closures? A `[]Mode` registry the tracker iterates on detach?
> - **Constructor injection vs registration.** Two shapes to consider:
>   - (A) `newAttach(modes []Mode, ...)` — explicit list passed in at construction; attach loop holds it.
>   - (B) `tracker.Register(mode Mode)` at startup; tracker is the central registry, attach loop just calls `tracker.ResetAll()` on detach.
>   - (C) Each mode is its own type that takes the relevant deps in its own constructor; attach composes them.
>   - Tradeoffs: testability, where the byte-sequence constants live, how the conditional sniffer logic plugs in (kitty kbd push/pop counting needs to inspect the output stream — that's stateful per-attach).
> - **OSC 9;4 specifically — does it need any tracking, or always-emit on detach?** Simplest: always-emit; if the inner program never set it, the clear is a no-op. If we want to be clever, sniff `OSC 9;4;1` and only emit clear if seen; probably overkill for one byte sequence, but consider.
> - **Where does the byte-sequence string live?** Today scattered as `seqXxx` package-level vars. Per-mode: each `Mode` owns its own constant. Discuss if there's a value to keeping a flat constants block vs distributing.
> - **Test shape.** Today `TestResetTermModesOrder` asserts a specific byte order. After refactor, the order should still be deterministic and asserted. Define the contract: maybe each mode declares an order priority (focus events before alt-screen exit before SGR reset before home+clear).
> - **Composition with other lifecycle bits.** `emitDetach` does more than reset modes — it shows a banner ("[disconnected. xxx @ host]"). Is that part of the same lifecycle, or stays separate? What about the supervisor-side `[detached by hoot detach]` notice — is that something `Mode` cares about? Probably no; it's already on its own path.
> - **Forward compat.** The codex spec's "Future Work" enumerates: app cursor keys (`CSI ? 1 l`), app keypad (`ESC >`), OSC palette (`OSC 104 BEL`), OSC dynamic colors (`OSC 110/111/112`), kitty keyboard stack policy. Sketch how each of those slots into the new shape — *without* doing them in this section. The point is a design that obviously generalizes.
> - **Scope of this section.** Land the refactor + the OSC 9;4 fix as the first concrete user of the new shape. Nothing else from "Future Work". Future work is filed as separate sections after this lands.
>
> ### Recommendation
>
> Pick a shape (A/B/C or another). Define the `Mode` (or whatever you call it) type. Sketch the implementation surface — file moves, new types, what the order-of-byte-emits guarantee looks like. Keep it small enough that the diff is "obvious refactor + one new mode for OSC 9;4", not a big-bang.
>
> ### Pre-plant rfc gate
>
> - [ ] rfc: review spec.md (refactor shape + OSC 9;4 mode)
> - [ ] implement per spec

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### phase 1: spec (rfc gate)

- [x] Read codex spec.md, attach.go, attach_term_modes_test.go, attach_fsm.go
- [x] Decide on Mode-interface shape vs codex's TerminalRestorer interface
- [x] Decide OSC 9;4 always-emit vs sniff-conditional (always-emit)
- [x] Decide where bytes live (per-mode, no flat block)
- [x] Decide ordering mechanism (construction-order via named fields)
- [x] Sketch per-file diff and test surface
- [x] Rewrite spec.md (push back on codex's overscoped "TerminalRestorer + MsgTerminalEvents")
- [x] **rfc gate** — ticked by boss at 2026-05-07T07:58Z

### phase 2: implement (post-rfc)

- [x] Create `cmd/hoot/attach_restorer.go`: interface + dtachRestorer + hootRestorer + comprehensive catalogue + constants (~290 lines). Commit 69df3f1.
- [x] Create `cmd/hoot/attach_restorer_test.go`: 13 tests including the catalogue-coverage guard. Commit 69df3f1.
- [x] Edit `cmd/hoot/attach.go`: added `--restorer` flag, thread through, replaced tracker with restorer, rewrote `emitDetach`, deleted ~250 lines of constants/tracker/helpers. Commit 69df3f1.
- [x] Update `cmd/hoot/run.go` and the existing tests (`attach_clone_test.go`, `attach_golden_test.go`, `run_attach_test.go`) for the new `runAttachLoop` / `attachOptionsFromFlags` / `runSession` signatures. Commit 69df3f1.
- [x] Delete `cmd/hoot/attach_term_modes_test.go`. Commit 69df3f1.
- [x] Add `cmd/hoot/attach_restorer_e2e_test.go` — full libghostty-on-both-ends attach-detach cycle, asserting via libghostty's native state queries that hoot's cleanup actually moves the user terminal back to defaults. Commit b7e72fe.
- [x] `go test ./cmd/hoot/` clean.
- [x] `go test ./...` clean.
- [x] Sanity-check the e2e test catches regressions: removed `seqCursorShow` from the cleanup list, confirmed test fails with `expected visible=true`; restored.
- [x] Updated README.md: document `--restorer` flag, replaced the old "8 mode resets" description with the comprehensive enumeration. Commit 0c0f246.

## Agent log

- 2026-05-07T13:50Z — Read codex spec, source, tests. Rewrote spec.md with a Mode-interface + named-field aggregator design. Pushed back on two pieces of codex's proposal: (1) the TerminalRestorer interface with two impls (no second consumer, YAGNI); (2) the server-side MsgTerminalEvents wire frame (out of scope, single consumer today). Kept codex's diagnosis intact in the appendix. The OSC 9;4 fix lands as a single new file `attach_mode_osc_progress.go` — that's the litmus test for the refactor.
- 2026-05-07T13:50Z — Setting status: blocked on the pre-planted rfc gate (`rfc: review spec.md`). Nothing else to do until the human ticks it. No code touched in the worktree yet.
- 2026-05-07T15:25Z — Human pushback: (a) does want two interchangeable attach/detach modes (so the `TerminalRestorer` interface direction was right; my "no second consumer / YAGNI" pushback was wrong); (b) ten files for one logical unit is too fine-grained. Pivoted spec to `TerminalRestorer` interface + two impls (`minimalRestorer`, `bestEffortRestorer`) in **one file** — modes are just lines in `Cleanup()`, not separate types. Also walked back on keeping the snapshot-sniff for kitty (adopted codex's always-push simplification: 4 invisible bytes per attach saves ~30 lines of separate parsing logic). Added a `--attach-mode` flag, default `best-effort` (behavior-preserving).
- 2026-05-07T15:35Z — Renamed: `--restorer hoot|dtach` (instead of `--attach-mode best-effort|minimal`); types `hootRestorer` and `dtachRestorer`. Anchored the experimental control to upstream `crigler/dtach`'s actual behavior (clear-on-attach + cursor-show-on-detach, byte-for-byte from `dtach/attach.c:220` and `:38`) rather than a designed-by-us "minimal." Symlinked ghostty + dtach into `repos/` for source-line accuracy.
- 2026-05-07T16:10Z — Human pivot: "no future work — do it as thoroughly as we can; scour ghostty for potential mode state cleanups." Read `~/github.com/ghostty-org/ghostty/src/terminal/modes.zig` end-to-end (40 mode entries, the canonical catalogue) plus `osc.zig` Command union plus `stream.zig` / `stream_terminal.zig` dispatch. Built the per-row catalogue table in spec.md with a decision for each entry (emit / observe / skip-default / skip-cursor-side-effect / skip-niche / skip-lease-required / already). Net additions to `hootRestorer.Cleanup()`: ~25 new byte-strings — biggest growth in mouse modes (2 → 8), keyboard input modes (2 → 11), display modes (0 → 11). Categorically NOT added: OSC palette + dynamic colors + window title + mouse shape + kitty colors — these require a save/restore lease (user's shell may set them pre-attach; unconditional reset clobbers user state; observation alone insufficient). Documented those in a "What we explicitly skip and why" section so they aren't misfiled as deferred Future Work. Added `TestHootRestorer_CleanupContainsAllCatalogueEntries` as the coverage-guard test that fails if the catalogue grows but `Cleanup()` doesn't.
- 2026-05-07T16:35Z — Two interface-contract cleanups prompted by further human pushback. (1) Dropped `sync.Once` from `Attach`: the once-only gate moves to `runServerLoop`'s existing `attached` atomic via `CompareAndSwap(false, true)`, restorers no longer carry a `once` field. (2) `Cleanup() string` → `Cleanup(w io.Writer) error`, symmetric with `Attach`; no accumulator, more honest typing for raw escape bytes. Also added a "Reconnects are transparent retry" decision section explicitly engaging with the "should reconnect = fresh attach/restore cycle?" question — answer is no for hoot today (would flicker on every drop; reconnect snapshot already re-establishes state), with a partial-cycle refinement (kitty pop+push round-trip per drop) filed as a follow-up if pop-count drift ever surfaces. Status stays `blocked` on the rfc gate.
- 2026-05-07T17:30Z — rfc gate ticked at 2026-05-07T07:58Z (boss log). Implementing per spec. Created `attach_restorer.go` (interface + dtach + hoot + comprehensive catalogue + constants) and `attach_restorer_test.go` (13 unit tests including the catalogue-coverage guard `TestHootRestorer_CleanupContainsAllCatalogueEntries`). Edited `attach.go` to add `--restorer` flag, replace tracker with restorer, rewrite `emitDetach` for writer-passing, delete ~250 lines of constants/tracker/helpers. Updated callers in `run.go` and the existing tests for the new signatures. Deleted `attach_term_modes_test.go`. Two commits: 69df3f1 (the structural refactor + comprehensive cleanup) and b7e72fe (e2e libghostty test).
- 2026-05-07T17:35Z — On the human's prompt, swapped my planned manual smoke todos for an e2e libghostty test. The `attachetest` package already wraps libghostty on both supervisor and user sides — `attachetest.Remote` (libghostty PTY supervisor) and `attachetest.LocalTerminal` (libghostty user terminal with native `ActiveScreen()`/`CursorVisible()` queries + a `WithFormatterExtraModes(true)` snapshot). Wrote `TestHootRestorer_E2EClearsCatalogueModes` that drives a full attach-detach cycle with a representative slice of catalogue modes set on the supervisor side, then asserts the user-side terminal is back to defaults via libghostty's native queries (not by re-parsing emitted bytes). Verified the test catches regressions: temporarily removed `seqCursorShow` from `hootCleanupSequence` → test fails with `expected visible=true`; restored. The e2e test is a much stronger evidence than manual smoke — runs in CI, catches the case where a constant string is wrong even though the slice contains it.
- 2026-05-07T17:40Z — Updated README.md to document `--restorer` and replace the old "8 mode resets" detach description with the comprehensive enumeration. Commit 0c0f246. All tests green: `go test ./...` passes for both worktree and main package.

- 2026-05-07T18:30Z — **Boss aborted `boss lgtm` on rebase conflict; resolving.** Master gained 5 merges since I branched, three of which touch `cmd/hoot/attach.go` heavily: (a) `e6e7d7f` hoot-write-subcommand (no overlap), (b) `e93c9cc` hoot-attach-tui-fuzzy-select-menu (added picker resolution + `--strict` flag in cmdAttach), (c) `e943910` hoot-extend-fzf-picker (lifted picker into shared `picker.go`/`resolveSessionKey`), (d) `7c93b53` hoot-better-hud-terminal-title (added `hudState` threaded through runAttachLoop/runConnectLoop/runSession + `hud.onAttach`/`hud.onDetach` lifecycle hooks), (e) `ac3f8f1` hoot-consolidate-serve-go (no overlap).
  - **Files conflicted:** `cmd/hoot/attach.go` (heavy — combined picker+HUD baseline), `cmd/hoot/run.go` (small — both sides added a 5th argument to `runAttachLoop`), `cmd/hoot/attach_clone_test.go` (small — both sides added a 5th argument), `README.md` (small — usage block + detach narrative).
  - **Resolution strategy per boss-log guidance:** for each conflicted file, took master's version end-to-end as the baseline (`git checkout --ours`), then re-applied my restorer changes on top. This preserves the picker + HUD features instead of clobbering them.
  - **Critical sequencing decision (attach-vs-HUD ordering).** Boss recommended `restorer.Attach` first then `hud.onAttach` at attach; reverse on detach. To make this work I had to **move `restorer.Attach` from `runServerLoop`'s first-snapshot gate to `runAttachLoop` entry** (after `term.MakeRaw`, before `hud.onAttach`). That's a real lifecycle change — see `## Trouble report` below for details on why I picked it.
  - **Resolution flow at attach** (in `runAttachLoop`): `term.MakeRaw` → `restorer.Attach(stdout)` (kitty kbd push for hoot mode; screen clear for dtach mode) → `hud.onAttach(stdout)` (title push + set) → `runConnectLoop` (dial + session). At detach (deferred): `hud.onDetach(stdout)` (title pop, unconditional) → if attached, `emitDetach(stdout, label, restorer)` which does `restorer.Cleanup(stdout)` + `[disconnected.]` banner.
  - **Subtle interactions noticed:**
    - **Lifecycle simplification**: with `restorer.Attach` at runAttachLoop entry, the `attached.CompareAndSwap(false, true)` gate I had in `runServerLoop` is gone. The restorer is naturally called once per process (runAttachLoop body runs once). No more `sync.Once` consideration.
    - **dtach mode semantics**: dtach-restorer's screen-clear now happens before the dial — same as `crigler/dtach` upstream, which clears at `attach.c:220` before `master_main_init`. If the dial fails, the user's pre-attach shell is cleared. That's faithful to dtach upstream and aligned with the "experimental control" intent.
    - **e2e test gap**: the `TestHootRestorer_E2EClearsCatalogueModes` test calls `runSession` directly (bypasses `runAttachLoop`). With restorer.Attach moved out of runServerLoop, the e2e test no longer exercised the kitty kbd push lifecycle. Fixed by calling `restorer.Attach(local)` explicitly before `runSession` in the test (commit 1555daa). Test now mirrors what runAttachLoop would do.
    - **HUD bytes don't appear in e2e snapshot**: e2e bypasses runAttachLoop, so no `hud.onAttach`/`hud.onDetach` ever runs in the test. Snapshot shows no title push/pop bytes. No assertions need to change.
  - **Tests after rebase**: `go test ./... -count=1` clean across all 5 packages (hootty / cmd/hoot / sessionpick / shortid / sshtransport). E2E test still catches regressions (verified with `seqCursorShow` removal earlier; same path).
  - **Branch commits after rebase**: `bbc9a7d` (refactor) → `767d11a` (e2e test) → `b26ac20` (README) → `1555daa` (post-rebase e2e fixup).

## Boss log
- 2026-05-07T07:58Z ticked: rfc
- 2026-05-07T12:46Z rebase conflict on master during `boss lgtm`. aborted — worktree is clean. flip status: working and resolve, please.
  
  # what landed on master since you branched
  
  base = `2acf3dd` (`hoot-remote-default-scheme-ssh`).
  
  four merges landed in this order — read all four, your branch overlaps with three of them:
  
  1. `e6e7d7f` Merge `hoot-write-subcommand-discuss-design`
     - 3 commits. touches: `pty_libghostty.go`, `pty_libghostty_routes.go` (new `?paste=on` query handling, `writeMu`, `BracketedPasteActive`); `cmd/hoot/serve.go` + `cmd/hoot/serve_spawn.go` (new `/sessions/{key}/input` proxy); new `cmd/hoot/encode.go`, `cmd/hoot/write.go` and tests; `cmd/hoot/main.go` (added `write` verb).
     - **conflict surface for you: probably zero.** You don't touch these files. Just take master's version of any auto-conflict on `cmd/hoot/main.go` usage block.
  
  2. `e93c9cc` Merge `hoot-attach-tui-fuzzy-select-menu-when-no-key-given`
     - 4 commits, `2dd454f` → `11ca906`. touches:
       - new `internal/sessionpick/{sessionpick.go,_test.go}` (Format + IDResolve helpers)
       - new `cmd/hoot/attach_picker.go` + `attach_picker_test.go` (later renamed)
       - `cmd/hoot/attach.go` heavy modification (picker resolve in `runConnectLoop`, no-args path drops into picker)
       - `README.md`
     - **conflict surface for you: medium-heavy in `cmd/hoot/attach.go`.** master's `attach.go` now has the picker-resolve flow and reaches `runConnectLoop` / `runSession` after a `resolveSessionKey` call. Your branch removed `terminalModeTracker` and changed those signatures to take `*Restorer` (or whatever the final shape is). need to: take master's picker-resolve flow as the structural baseline, then re-apply your restorer-related signature changes.
  
  3. `e943910` Merge `hoot-extend-fzf-picker-to-clone-kill-detach-update-help-strings`
     - 4 commits, `dfee6bc` → `9dbbde6`. touches:
       - **`cmd/hoot/attach_picker.go` deleted; renamed/replaced by `cmd/hoot/picker.go`** (lifted into shared `runFZFPicker` + `resolveSessionKey` + `loadSessionList` with `pickerOptions{Verb}`).
       - `cmd/hoot/attach_picker_test.go` renamed → `cmd/hoot/picker_test.go`. New `cmd/hoot/picker_resolve_test.go`.
       - `cmd/hoot/clone.go`, `cmd/hoot/clone_test.go`, `cmd/hoot/detach.go`, `cmd/hoot/kill.go` — all gained `--strict` + picker fallback.
       - `cmd/hoot/main.go` usage block updated.
       - `README.md`
     - **conflict surface for you: low-medium.** mostly file moves you adopt as-is (keep master's `picker.go` etc.). your branch doesn't touch clone/kill/detach so those stay master's. main.go usage block: take master's, add your `--restorer` flag-help line on top.
  
  4. `7c93b53` Merge `hoot-better-hud-terminal-title-status-line-chord-on-attach`
     - 4 commits, `e9bbf1f` → `b1f06ea`. touches:
       - new `cmd/hoot/title.go` + `title_test.go` (HUD title push/pop + status-line chord via existing prefix-chord dispatcher; `hudState` struct holds onAttach/onDetach lifecycle).
       - `internal/sessionpick/sessionpick.go` + tests (added `FormatHUD`).
       - `cmd/hoot/attach.go` — wires `hudState` through `runAttachLoop` / `runConnectLoop` / `runSession`; calls `hud.onAttach(w)` after `term.MakeRaw`, calls `hud.onDetach(w)` in `emitDetach` family of calls.
       - `cmd/hoot/run.go`, `cmd/hoot/run_attach_test.go`, `cmd/hoot/attach_clone_test.go` — signature updates for the HUD threading.
       - `README.md`
     - **conflict surface for you: HEAVY in `cmd/hoot/attach.go`.** master now has TWO things plumbed through `runAttachLoop` / `runConnectLoop` / `runSession`: the picker resolution result AND the `hudState`. Your branch is plumbing through a third thing (the `Restorer`). Take master's combined signatures as the baseline, then add `Restorer` alongside `hudState` (probably an additional parameter or a small struct that bundles both).
       - **Critical interaction**: `hudState.onAttach(w)` and your `restorer.Attach(w)` both write bytes to the user terminal at attach time. Sequence them deterministically: I would put **`restorer.Attach` first** (does the kitty-kbd push and dtach screen-clear if applicable), then `hud.onAttach` (sets the title, owns its own escape sequences). Reverse on detach: `hud.onDetach` first (pops title), then `restorer.Cleanup(w)` (the comprehensive mode-reset block). Reasoning: the title push/pop is purely cosmetic UI state; the restorer is owns the inner-program-affecting modes. Doing restorer-then-hud at attach means the HUD title gets set on a "clean slate"; doing hud-first-then-restorer at detach means the title pops before we reset all the modes (so a future title query during reset isn't affected by the in-flight reset).
       - **NOTE**: this is a judgment call. If you have a better argument for a different ordering, take it — but document your reasoning in `## Agent log`.
  
  # resolution guidance (paste verbatim — important)
  
  resolve by **preserving features from master, not by taking your side blindly**. for each conflicted file, read master's version end-to-end first and understand what features the new lines implement before overwriting. if master's changes are a superset of what you did, adopt master's version and re-apply your delta on top. re-run tests after resolution.
  
  specific files to expect heavy work:
  
  - `cmd/hoot/attach.go`: take master's combined picker+HUD baseline. re-apply `Restorer` plumbing on top. Sequence `restorer.Attach` → `hud.onAttach` at attach time; `hud.onDetach` → `restorer.Cleanup` at detach time. Update `runAttachLoop`/`runConnectLoop`/`runSession` signatures to accept the new third parameter alongside picker-result and hudState.
  - `cmd/hoot/run.go`: master added HUD threading. you also touched it for restorer threading. take master's HUD shape, add restorer alongside.
  - `cmd/hoot/run_attach_test.go`, `cmd/hoot/attach_clone_test.go`: same combine.
  - `cmd/hoot/main.go` usage block: take master's, append your `--restorer` line.
  - `README.md`: master rewrote attach narrative to mention HUD + picker. take master's prose; add your `--restorer` documentation as an additional paragraph.
  - **deleted/renamed files**: `cmd/hoot/attach_picker.go` is gone (now `picker.go`); `cmd/hoot/attach_picker_test.go` is now `picker_test.go`. don't try to keep your old paths.
  - **new files from master (untouched by you, keep as-is)**: `cmd/hoot/title.go`, `cmd/hoot/title_test.go`, `cmd/hoot/picker.go`, `cmd/hoot/picker_resolve_test.go`, `cmd/hoot/encode.go`, `cmd/hoot/encode_test.go`, `cmd/hoot/write.go`, `cmd/hoot/write_test.go`.
  - **your new files** (`attach_restorer.go`, `attach_restorer_test.go`): ship as-is, no conflict.
  - **your deleted file** (`attach_term_modes_test.go`): stays deleted per spec; if master added test cases there the answer is they migrate to per-restorer tests in `attach_restorer_test.go`.
  
  # expected next actions
  
  - flip `status: working` in your worklog frontmatter.
  - resolve the rebase, update commit(s) (or new commit, your call).
  - append a substantial note to `## Agent log` summarizing: which files conflicted, the attach-vs-hud sequencing decision you made, and any subtle interactions you noticed.
  - run `go test -count=1 ./...` clean.
  - particularly important: re-run your e2e libghostty test (`TestHootRestorer_E2EClearsCatalogueModes`) — if HUD title push/pop now sits in the lifecycle, the test needs to either ignore those bytes or assert they happen.
  - flip `status: done`.
  - i'll re-run `boss lgtm`.
  
  if the rebase conflicts again or the test surface is more tangled than expected, append the new context to `## Agent log` with what you saw — don't keep retrying blindly.

## Evidence

### Commits on this branch (post-rebase onto current master `ac3f8f1`)

```
1555daa e2e test: mirror new runAttachLoop restorer.Attach lifecycle
b26ac20 README: document --restorer flag and comprehensive detach cleanup
767d11a Add e2e libghostty test for hootRestorer cleanup
bbc9a7d Refactor attach detach cleanup into TerminalRestorer interface; add --restorer flag; comprehensive Ghostty mode cleanup
```

### Unit-test run

`go test ./cmd/hoot/ -count=1 -v -run 'TestNewRestorer|TestDtachRestorer|TestHootRestorer|TestEmitDetach'` (verbose, all 14 restorer-related tests):

```
=== RUN   TestNewRestorer
--- PASS: TestNewRestorer (0.00s)
=== RUN   TestDtachRestorer_AttachClearsScreen
--- PASS: TestDtachRestorer_AttachClearsScreen (0.00s)
=== RUN   TestDtachRestorer_Cleanup
--- PASS: TestDtachRestorer_Cleanup (0.00s)
=== RUN   TestDtachRestorer_ObserveIsNoOp
--- PASS: TestDtachRestorer_ObserveIsNoOp (0.00s)
=== RUN   TestHootRestorer_AttachPushesKittyFrame
--- PASS: TestHootRestorer_AttachPushesKittyFrame (0.00s)
=== RUN   TestHootRestorer_AttachWriteErrorPreservesState
--- PASS: TestHootRestorer_AttachWriteErrorPreservesState (0.00s)
=== RUN   TestHootRestorer_CleanupAfterAttach
--- PASS: TestHootRestorer_CleanupAfterAttach (0.00s)
=== RUN   TestHootRestorer_CleanupClearsOSCProgress
--- PASS: TestHootRestorer_CleanupClearsOSCProgress (0.00s)
=== RUN   TestHootRestorer_CleanupContainsAllCatalogueEntries
--- PASS: TestHootRestorer_CleanupContainsAllCatalogueEntries (0.00s)
=== RUN   TestHootRestorer_ObservesKittyPushPop
--- PASS: TestHootRestorer_ObservesKittyPushPop (0.00s)
=== RUN   TestHootRestorer_ObservesModifyOtherKeys
--- PASS: TestHootRestorer_ObservesModifyOtherKeys (0.00s)
=== RUN   TestHootRestorer_ObservesSplitCSI
--- PASS: TestHootRestorer_ObservesSplitCSI (0.00s)
=== RUN   TestEmitDetachUsesCleanupBeforeBanner
--- PASS: TestEmitDetachUsesCleanupBeforeBanner (0.00s)
=== RUN   TestEmitDetachWithNilRestorer
--- PASS: TestEmitDetachWithNilRestorer (0.00s)
PASS
ok  	github.com/hayeah/hootty/cmd/hoot	0.276s
```

### E2E libghostty test

`go test ./cmd/hoot/ -count=1 -v -run E2E`:

```
=== RUN   TestHootRestorer_E2EClearsCatalogueModes
--- PASS: TestHootRestorer_E2EClearsCatalogueModes (0.03s)
PASS
ok  	github.com/hayeah/hootty/cmd/hoot	0.505s
```

This test is the load-bearing piece of evidence:

- Spins up `attachetest.Remote` (libghostty PTY) and `attachetest.LocalTerminal` (libghostty user-side).
- Sets a representative slice of the catalogue on the supervisor: `?1049 ?1004 ?1000 ?1002 ?1006 ?2004 ?2026 ?25l >4;2m >u OSC9;4;1`.
- Drives `runSession` (the real hoot attach loop, including snapshot delivery, kitty kbd push at attach via `restorer.Attach`, observer running on each chunk).
- Verifies mid-attach that the user-side libghostty has actually adopted the modes (`active=alternate`, `visible=false`, formatVT shows `?1004h ?1000h ?1049h`).
- Closes the session and runs `emitDetach(local, ..., restorer)` — same path as a real detach.
- Verifies post-cleanup via libghostty's native queries: `active=primary`, `visible=true`, formatVT no longer contains any of the mode-set sequences.

Verified to catch regressions: removed `seqCursorShow` from `hootCleanupSequence` → test fails with `post-cleanup: expected visible=true, snapshot: ... visible=false`. Restored.

### Sanity-check the catalogue-coverage guard

`TestHootRestorer_CleanupContainsAllCatalogueEntries` walks an explicit `catalogue` list (independent from the production `hootCleanupSequence` slice) and asserts every entry appears in `Cleanup()`'s captured output. Also checks the two lists are the same length, so a production-side addition without a test-side addition (or vice versa) fails the test. This is the discipline that backs the spec's "decide once, record the decision, test enforces."

### Full repo-wide test pass (post-rebase)

`go test ./... -count=1`:

```
ok  	github.com/hayeah/hootty	6.960s
ok  	github.com/hayeah/hootty/cmd/hoot	5.908s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.549s
ok  	github.com/hayeah/hootty/internal/shortid	0.424s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.732s
```

All green, including master's new tests (sessionpick, picker_resolve_test, title_test) — no downstream breakage from the picker+HUD+restorer signature combination.

### Diff stat

```
 8 files changed, 879 insertions(+), 390 deletions(-)
 cmd/hoot/attach.go             | -250 lines (constants block, terminalModeTracker, helpers)
 cmd/hoot/attach_restorer.go         | +290 (NEW: interface + 2 impls + constants)
 cmd/hoot/attach_restorer_test.go    | +260 (NEW: 13 unit tests)
 cmd/hoot/attach_restorer_e2e_test.go| +142 (NEW: libghostty e2e test)
 cmd/hoot/attach_term_modes_test.go  | -100 (DELETED, subsumed)
 cmd/hoot/{run,attach_clone_test,attach_golden_test,run_attach_test}.go | small wiring changes
 README.md                            | +28 -9 (--restorer doc + comprehensive cleanup desc)
```

## Trouble report

- The codex spec.md is internally split: an early "Recommendation" pushing the small `seqOSCProgressClear` slot-in, and a later "Proposed Terminal Restoration API" pushing a much larger refactor (interface + two impls + central server-side event scanner + new wire frame). The picked design landed somewhere in the middle: `TerminalRestorer` interface (codex was right, my "no second consumer" was wrong), but no per-mode types, no central event scanner, and no new wire frame.
- The spec went through several rev cycles before the rfc gate ticked: initial `Mode`-interface + named-field aggregator (10 files) → `TerminalRestorer` + 2 impls in 1 file (renamed minimal/best-effort to dtach/hoot) → comprehensive Ghostty mode catalogue (no Future Work) → cleanup of the lifecycle wording (drop `sync.Once`, switch `Cleanup() string` to `Cleanup(w) error`). Each rev's tradeoffs are recorded in spec.md's `## Design notes`.
- No `.worktrees.setup` hook on the hootty repo. No dev services in this section.
- The `attachetest` package's libghostty-on-both-ends harness already existed (used by the golden tests) — I just had to wire a new test on top of it. That made the e2e test a ~140-line addition rather than a multi-day infra build. Worth flagging that this kind of harness is the right shape for any future "actually applied to the terminal" assertion.
- The `\x1b[?1049 l` cleanup looks redundant once `\x1b[?47 l` and `\x1b[?1047 l` are in the catalogue — libghostty's alt-screen state is shared, so any of the three fires it. Kept all three because (a) terminals other than Ghostty may not share state across the three vars, (b) the catalogue's invariant is "for every catalogue row, the corresponding reset is emitted." Reasoning is in the catalogue and decision sections of spec.md.

- **Lifecycle pivot mid-rebase: `restorer.Attach` moved from runServerLoop's first-snapshot gate to runAttachLoop entry.** Original spec parked `restorer.Attach` in `runServerLoop` (called once on the first MsgSnapshot via an `attached.CompareAndSwap(false, true)` gate). When master gained the HUD machinery — which calls `hud.onAttach` immediately after `term.MakeRaw` in `runAttachLoop` — the boss recommended sequencing `restorer.Attach` BEFORE `hud.onAttach` at the same lifecycle point. To make that work, I had to move `restorer.Attach` out of runServerLoop and up into runAttachLoop entry. Tradeoffs:
  - **Pro**: cleaner lifecycle (no `attached.CompareAndSwap` gate). Restorer is naturally once-per-process. Symmetric with `hud.onAttach`.
  - **Pro**: dtach mode now matches `crigler/dtach/attach.c:220` upstream byte-for-byte (clear-on-attach BEFORE master connect, regardless of dial outcome).
  - **Con**: dtach mode clears the user's pre-attach shell content even if the dial fails. Faithful to upstream dtach (which does the same), but a regression vs my original spec where the clear only happened after first snapshot. Trading a marginal UX edge case for upstream-fidelity feels right for an experimental control variant.
  - **Con**: spec.md's "Reconnects are transparent retry" decision section talks about the `CompareAndSwap` gate that no longer exists. Spec is slightly stale here. Acceptable: the underlying property (Attach once per process, no per-reconnect re-Attach) is preserved by the new shape (runAttachLoop body runs once).
  - **e2e test gap**: the test calls `runSession` directly; it never enters runAttachLoop. So restorer.Attach wasn't being called in the test, leaving kitty kbd push behavior uncovered. Fixed by adding an explicit `restorer.Attach(local)` call before `runSession` (commit 1555daa). Should have caught this earlier; flagging it because "test calls subset of the lifecycle" is a real gap in the e2e harness's faithfulness.
