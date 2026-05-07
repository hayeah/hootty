---
status: blocked
section: Hoot detach chord — kitty keyboard regression with Codex
slug: hoot-detach-chord-kitty-keyboard-regression-with-codex
mode: worktree
spec: spec.md
created: 2026-05-07T13:08:32Z
---

> ## Hoot detach chord — kitty keyboard regression with Codex
>
> ---
> status:
>   type: open
> ---
>
> Spec mode. Spawn with `--agent codex` (the human asked for codex). Investigate the failure, document findings + recommendation in `spec.md`, park at rfc, then implement.
>
> ### Symptom
>
> `C-^ .` (default detach chord) works in Ghostty + Claude Code (post-`5373109` fix), still works in iTerm + everything (legacy bytes), but **does not work in Ghostty or kitty when the inner program is Codex** running as `pnpm dlx @openai/codex --dangerously-bypass-approvals-and-sandbox`. iTerm + Codex still works.
>
> The user can detach via the prefix-key chord in iTerm, but not in Ghostty or kitty.
>
> ### Background you need to read first
>
> - `~/Dropbox/notes/2026-05-06/hoot-detach-key-kitty-keyboard-protocol_claude.md` — the original spec for the kitty-kbd CSI u matcher work.
> - Original commit: `5373109` "Recognize kitty CSI u encoding of detach prefix and chord follow-up".
> - Current matcher: `cmd/hoot/attach_fsm.go` — `parseCSIuParams`, `isPrefixKey`, `runChordFSM`. Existing tests: `attach_fsm_test.go` (or wherever `TestRunChordFSM*`/`TestParseCSIuParams`/`TestIsPrefixKey` live).
>
> The existing fix accepts `CSI 94;6 u` (`^` + ctrl+shift), `CSI 54;6 u` (`6` + ctrl+shift), alt-key form `CSI 94:54;6 u`, and chord follow-up `CSI 46 u` (`.` no modifier).
>
> ### Hypothesis (push back if you find otherwise)
>
> Codex likely enables a **higher kitty keyboard progressive-enhancement level** than Claude Code. Kitty mode flags (`CSI > N u`, OR'd):
>
> - `1` disambiguate escape codes (basic — Claude Code probably uses this)
> - `2` report event types → adds **press/release/repeat** events. The `mod` parameter gets a colon-separated event tag: `\e[CODEPOINT ; MOD:EVENT u` where EVENT = 1 (press), 2 (repeat), 3 (release).
> - `4` report alternate keys (colon syntax in codepoint param — already handled)
> - `8` report all keys as escape codes — even bare `.` becomes `CSI 46 u`
> - `16` associated text — appends a `;text` parameter
>
> Two likely failure modes:
>
> 1. **Release events confuse the FSM.** Codex pushes `>3u` or `>5u` etc. with `report_event_types` enabled. When the user types `C-^`, the terminal sends both a press (`\e[94;6:1u`) and a release (`\e[94;6:3u`). The FSM might match on either and get into a weird state, or the chord byte arrives between them, etc.
> 2. **Chord follow-up encoding changed.** With `report_all_keys_as_escape_codes` (bit `8`), the `.` is no longer a bare `0x2e` byte but `CSI 46 u`. Today's FSM's chord-byte matcher in step 2 (after seeing the prefix) might only check for the legacy single byte. If we already accept CSI u for `.` per the earlier fix — verify — but if the `.` now arrives with a modifier-colon-event tag (`\e[46;1:1u` press), the modifier parser might trip.
>
> ### Investigate
>
> - **Find what Codex actually pushes.** Read codex's source at `~/github.com/openai/codex` (or wherever it lives — `pnpm dlx @openai/codex` ships from npm; clone the GitHub repo for source). Search for `kitty`, `keyboard_protocol`, `progressive`, `>1u`, `>3u`, `report_event_types`. Whatever flags it OR's, that's the answer. Cite file:line.
> - **Reproduce live.** Spawn a hoot session running codex inside Ghostty (or kitty); attach; press `C-^ .`; capture the actual bytes hoot sees on stdin. Compare with Claude Code's stream. The user said this is reproducible — so reproduce it, don't just theorize.
>   - Tip: hoot's input flows through `cmd/hoot/attach.go`'s `clientReadLoop`. Add a temporary stderr log of every input byte if needed for the trace; revert before commit. Or use `script(1)` / `tmux capture-pane` on the local terminal pre-hoot to see what Ghostty/kitty would emit.
>   - Or: run a tiny Go program that just enables `>3u` / `>5u` / `>9u` and prints the bytes received for `C-^ .`. That isolates the terminal-side encoding from hoot.
> - **Trace the FSM.** With the captured byte sequence, walk through `runChordFSM` step-by-step. Identify the exact byte/state where the match fails.
> - **Why it works in iTerm.** iTerm has incomplete kitty support — same as before. It silently drops the `>Nu` mode push or honors only level 1. So the legacy bytes still arrive, the legacy matcher still fires.
>
> ### Spec.md
>
> Document:
> - The exact mode flags Codex pushes (with source pointer).
> - The exact byte sequence the user's terminal emits for `C-^ .` under those flags, in Ghostty AND kitty (different terminals can implement the protocol slightly differently).
> - The exact place(s) in `runChordFSM` / `parseCSIuParams` / `isPrefixKey` that don't match.
> - Recommendation: extend the FSM/parser to handle the new variant(s). Specifically:
>   - If `mod:event` colon-syntax is the issue: parse it, treat press events the same as today, **drop release/repeat for chord matching** (release of the prefix key shouldn't cancel the chord; a press of the wrong key after the prefix should still cancel). Document the press-only policy.
>   - If the chord follow-up is now CSI u: confirm today's matcher accepts it; add a test for the colon-event variant.
>   - Anything else surfaced by the trace.
> - New unit tests for each variant.
> - Manual smoke recipe for verifying the fix in Ghostty + kitty + iTerm against Codex.
>
> ### Pre-plant rfc gate
>
> - [ ] rfc: review spec.md (diagnosis + matcher extension)
> - [ ] implement per spec — include unit tests covering the new variants, run full suite, document smoke verification
>
> If the diagnosis turns out to be different from the hypothesis, push back in `spec.md` with the actual finding. Don't shoehorn.

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Check out required repos through `boss checkout`
- [x] Read the prior kitty keyboard protocol note and current hoot FSM/tests
- [x] Identify the Codex kitty keyboard flags from source
- [x] Trace why the current FSM fails under Codex event reporting
- [x] Draft `spec.md` with diagnosis, recommendation, tests, and smoke plan
- [x] rfc: review `spec.md` diagnosis and event-aware matcher policy
- [x] Implement event-aware CSI u parsing and FSM handling
- [x] Add unit tests for press/release/repeat variants and Codex regression stream
- [x] Run focused and full hoot test suites
- [ ] Document manual Ghostty, kitty, and iTerm smoke evidence

## Agent log
- 2026-05-07T13:18:50Z - Checked out `hayeah/hootty`, `openai/codex`, `ghostty-org/ghostty`, and `kovidgoyal/kitty` via `boss checkout`; drafted `spec.md` and parked at the planned RFC gate.
- 2026-05-07T13:19Z blocked: rfc review spec.md diagnosis and event-aware matcher policy; live GUI byte capture was blocked by macOS accessibility, source-derived traces are documented
- 2026-05-07T13:43Z - Implemented event-aware CSI u parsing/FSM handling and regression tests in hootty commit `09cfd84`.
- 2026-05-07T13:46Z - blocked: automated verification is complete, but manual Ghostty/kitty/iTerm smoke remains. `osascript` key injection hung earlier, `cliclick` reports Accessibility privileges disabled, and no local kitty binary/app is installed.
- 2026-05-07T13:43Z landed event-aware CSI u parser/FSM handling + Codex regression tests in hootty 09cfd84; focused, cmd/hoot, and repo-wide Go tests pass
- 2026-05-07T13:43Z blocked: automated verification complete for 09cfd84, but manual Ghostty/kitty/iTerm smoke needs human GUI access; osascript hung, cliclick lacks Accessibility privileges, and kitty is not installed

## Boss log
- 2026-05-07T13:40Z ticked: rfc

## Evidence

RFC evidence so far:

- Codex source shows `DISAMBIGUATE_ESCAPE_CODES | REPORT_EVENT_TYPES | REPORT_ALTERNATE_KEYS` in `codex-rs/tui/src/tui.rs:72-79`, i.e. kitty flags `7`.
- Existing focused tests pass before implementation: `go test ./cmd/hoot -run 'TestRunChordFSM|TestParseCSIuParams|TestIsPrefixKey' -v`.
- `spec.md` contains the FSM trace and recommended matcher extension.

Implementation evidence:

- Commit: `09cfd84` (`Handle kitty CSI u release events in detach FSM`)
- Focused FSM suite: `go test ./cmd/hoot -run 'TestRunChordFSM|TestParseCSIuParams|TestIsPrefixKey' -v` passed.
- Full hoot package suite: `go test ./cmd/hoot -v` passed.
- Repo-wide Go suite: `go test ./...` passed.
- Manual smoke is not complete; blocked on a human-accessible Ghostty/kitty/iTerm run or enabling local GUI automation privileges.

## Trouble report

- Live Ghostty byte capture could not be completed from this agent session because `osascript` hung while trying to inject keys through macOS `System Events`, likely due to accessibility permissions. `spec.md` uses terminal source-derived byte streams and asks whether to proceed from that or wait for human-provided live captures.
- `cliclick -h` confirms the same class of blocker: "Accessibility privileges not enabled." Also, `kitty` is not installed under PATH or `/Applications`, so I cannot run the requested kitty smoke locally.
