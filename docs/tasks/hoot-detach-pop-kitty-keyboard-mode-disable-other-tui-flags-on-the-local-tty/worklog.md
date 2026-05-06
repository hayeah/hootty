---
status: working
section: hoot detach: pop kitty keyboard mode + disable other TUI flags on the local tty
slug: hoot-detach-pop-kitty-keyboard-mode-disable-other-tui-flags-on-the-local-tty
mode: worktree
spec:
created: 2026-05-06T05:05:29Z
---

> ## hoot detach: pop kitty keyboard mode + disable other TUI flags on the local tty
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Implement the spec at `/Users/me/Dropbox/notes/2026-05-06/hoot-detach-kitty-keyboard-leak_claude.md`. Read it end-to-end first — it's long but the actual change is small and the doc explains *why* each sequence matters.
>
> TL;DR: `hoot attach` detaches by writing a small reset to the local tty. Today that reset only handles SGR / cursor / alt-screen / clear. It misses the kitty keyboard pop (`CSI < u`), focus events (`DECRST 1004`), bracketed paste (`DECRST 2004`), and mouse tracking (`DECRST 1006/1000`). When the inner TUI (claude, helix, vim, etc.) pushed those modes during the session, they leak into the local terminal on detach — keypresses arrive at the outer shell as `CSI <code>;<mods> u` reports, etc.
>
> The spec proposes a `resetTermModes` constant assembled from per-mode named segments, replacing the inline `Fprintf` at `cmd/hoot/attach.go:720` with `resetTermModes + disconnect banner`. Use the named-constant structure verbatim from the spec — the comments are load-bearing.
>
> ### Open call: paranoid pop?
>
> The spec mentions `CSI < 5 u` (pop up to 5 levels) as a "paranoid" alternative to a single `CSI < u`. Recommendation: ship the single-pop default since it matches the typical one-TUI case; if you have a strong reason to go paranoid, mention rationale in agent log.
>
> ### Verification
>
> The reset goes to the **local** tty — easy to test in isolation. No need for a real remote attach to demonstrate the leak; can drive the detach path directly.
>
> - Unit / integration test: feed the detach-write code path a recording `io.Writer`, assert the bytes contain each pop/disable sequence in the documented order.
> - Manual: from a kitty / Ghostty / wezterm terminal, attach to a session that's running a kitty-kbd-aware TUI (claude is the canonical repro), detach via `<prefix>.`, type a few characters at the resulting prompt — should be plain text, no `;u` tails.
> - Smoke transcript saved to `tmp/`.
>
> ### Procedure
>
> - [ ] implement per spec, add tests, manual smoke from a local kitty-kbd-supporting terminal, ship
>   - evidence: golden-style assertion that `resetTermModes` contains each documented sequence
>   - evidence: post-fix manual transcript showing clean keystrokes after detach from a TUI session
>   - evidence: README / docs update if the spec implies one (the spec doesn't strongly require it; agent's call)
>
> Use codex.

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->
- [x] Read worklog and both 2026-05-06 hoot kitty keyboard notes
- [x] Add local detach reset sequences with named per-mode constants
- [x] Include libghostty keyboard and kitty keyboard state in snapshots
- [x] Make kitty keyboard pop conditional on local observed/created stack frames
- [x] Add byte-level tests for detach reset order, conditional kitty pop, and snapshot keyboard state
- [x] Run automated Go tests
- [ ] Manual kitty/Ghostty/wezterm detach smoke from an interactive local terminal

## Agent log
- 2026-05-06T05:11Z — Implemented Fix A and the boss note's Fix B in `~/github.com/hayeah/hootty`.
  - `cmd/hoot/attach.go`: replaced inline detach cleanup with named reset segments. Later corrected the kitty keyboard part to be tracked/conditional rather than part of unconditional `resetTermModes`.
  - `pty_libghostty.go`: added `WithFormatterExtraKeyboard(true)` and `WithFormatterExtraKittyKeyboard(true)` so snapshots restore modify-other-keys and kitty keyboard state on attach.
  - Tests added: `TestResetTermModesOrder`, `TestEmitDetachWritesModeResetBeforeBanner`, `TestFormatTerminalSnapshotIncludesKeyboardModes`.
  - Docs updated: README attach/detach terminal mode paragraph now mentions keyboard snapshot restore and local detach cleanup.
  - Automated verification: `go test ./cmd/hoot -run 'TestResetTermModesOrder|TestEmitDetachWritesModeResetBeforeBanner|TestAttachGoldenSnapshots/detach'`, `go test . -run TestFormatTerminalSnapshotIncludesKeyboardModes`, and `go test ./...` all passed.
  - Manual smoke not run here: command runner is not attached to an interactive terminal (`tty` reports `not a tty`), so I cannot drive a real kitty/Ghostty/wezterm attach/detach session from this environment.
- 2026-05-06T05:28Z — Addressed concern that unconditional `CSI < u` is not symmetric with libghostty's snapshot output.
  - Confirmed from the shipping note and Ghostty source that `WithFormatterExtraKittyKeyboard(true)` emits `CSI = <flags>;1u`, an apply-to-current-level operation, not a push.
  - Changed attach handling so a snapshot containing kitty keyboard state first writes a hoot-owned empty push (`CSI > u`) to the local tty, then lets the snapshot's `CSI = ... u` apply to that frame.
  - Added a small local terminal mode tracker. It tracks hoot-created snapshot frames plus live kitty push/pop sequences across chunk boundaries, and detach pops only the outstanding tracked count.
  - Added conditional modifyOtherKeys cleanup: `CSI > 4;0m` is emitted only if the attach stream observed `CSI > 4;2m`.
  - Added tests for no-pop-by-default, snapshot-push-then-pop, live push/pop balancing, split CSI push detection, live pop consuming the snapshot frame, and modifyOtherKeys reset.
  - Re-ran focused tests and `go test ./...`; all passed.

## Boss log
- 2026-05-06T05:07Z hi — your spawn briefing got dropped by the readiness flake (the agentboss fix landed at 533dffb but the installed binary isn't refreshed yet). you (codex, hn9) are the spawned worker.
  
  read AGENT_LOOP and your worklog.md. the section text quoted at the top points at a detailed spec at /Users/me/Dropbox/notes/2026-05-06/hoot-detach-kitty-keyboard-leak_claude.md. read both end-to-end, then implement.
  
  go.

## Evidence
- `go test ./cmd/hoot -run 'TestResetTermModesOrder|TestEmitDetachWritesModeResetBeforeBanner|TestAttachGoldenSnapshots/detach'` — pass
- `go test . -run TestFormatTerminalSnapshotIncludesKeyboardModes` — pass
- `go test ./...` — pass
- `go test -count=1 ./cmd/hoot -run 'TestResetTermModesOrder|TestEmitDetachWritesModeResetBeforeBanner|TestTerminalModeTracker|TestAttachGoldenSnapshots/detach'` — pass
- `go test -count=1 . -run TestFormatTerminalSnapshotIncludesKeyboardModes` — pass
- Manual interactive smoke pending because this runner has no local tty.

## Trouble report
