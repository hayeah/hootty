# Hoot detach chord under Codex kitty keyboard mode

## Goal

Fix hoot's default detach chord, `C-^ .`, when the attached program is Codex inside a terminal that honors the kitty keyboard protocol. Codex enables event-type reporting, so the terminal sends a release event for the `C-^` prefix. Today hoot parses that release as if it were another prefix press and consumes the chord window before `.` arrives. The implementation should preserve legacy iTerm behavior and the `5373109` CSI u support while adding event-aware matching.

Out of scope: changing Codex, disabling kitty keyboard mode, or stripping kitty keyboard mode sequences before they reach the outer terminal.

## Diagnosis

Codex pushes kitty keyboard flags `7`: disambiguate escape codes (`1`), report event types (`2`), and report alternate keys (`4`).

- Codex TUI source: `repos/github.com/openai/codex/codex-rs/tui/src/tui.rs:72-79`
- Codex cloud task UI source uses the same flags: `repos/github.com/openai/codex/codex-rs/cloud-tasks/src/lib.rs:779-786`
- Codex does not request report-all (`8`) or associated text (`16`), so bare printable `.` normally remains a literal `0x2e`, not `CSI 46 u`.

Under flags `7`, both kitty and Ghostty send press/release event reports for modified keys. Source-derived expected streams for a US-layout `C-^` typed as `Ctrl+Shift+6`, then released, then `.`:

- Ghostty: `\x1b[54:94;6u\x1b[54:94;6:3u.`
  - `54` is the unshifted key (`6`), `94` is the shifted alternate (`^`), `6` is the kitty modifier value for `ctrl+shift` (`1 + 4 + 1`), and `:3` is release.
  - Ghostty uses the unshifted codepoint as the primary CSI u key (`src/input/key_encode.zig:137-145`), adds shifted alternates (`src/input/key_encode.zig:254-281`), emits press without `:1` but release with `:3` (`src/input/key_encode.zig:992-1037`), and maps modifier bits as kitty `mod+1` (`src/input/key_encode.zig:861-865`).
- kitty: `\x1b[54:94;6u\x1b[54:94;6:3u.`
  - kitty's macOS path maps the physical key to the unmodified layout key (`glfw/cocoa_window.m:328-353`), derives alternate keys on key down/up (`glfw/cocoa_window.m:1235-1250`, `glfw/cocoa_window.m:1360-1361`, `glfw/cocoa_window.m:1430-1437`), serializes alternates in the key field (`kitty/key_encoding.c:72-82`), and adds `:3` only for release/repeat events (`kitty/key_encoding.c:52-58`, `kitty/key_encoding.c:376-405`, `kitty/key_encoding.c:423-431`).

I attempted a live Ghostty capture using `tmp/capture-kitty-keybytes.sh`, launched under `/Applications/Ghostty.app`, but GUI key injection via `osascript System Events` hung on macOS accessibility permissions. That means the exact byte strings above are source-derived, not confirmed by a live keypress in this agent session. The manual smoke recipe below should capture the live bytes after implementation.

## Failure Trace

Current parser state:

- `keyEvent` stores only `raw`, `csiU`, `cp`, `altCp`, and `mod` (`cmd/hoot/attach_fsm.go:35-43`).
- `parseCSIuParams` documents `;mod:event` but discards the event tail by splitting the second group and keeping only `mod` (`cmd/hoot/attach_fsm.go:94-110`).
- Existing parser tests assert that `94;6:1` becomes `(94, 0, 6)`, proving the event is intentionally lost today (`cmd/hoot/attach_fsm_test.go:158-179`).

FSM walk for `\x1b[54:94;6u\x1b[54:94;6:3u.`:

- `stateNormal` reads the press `CSI 54:94;6 u`.
- `isPrefixKey` sees ctrl in `mod=6`, sees `54` or `94` in the prefix codepoint set, and moves to `stateAfterPref` (`cmd/hoot/attach_fsm.go:187-201`, `cmd/hoot/attach_fsm.go:288-292`).
- `stateAfterPref` reads the release `CSI 54:94;6:3 u`.
- Because the event was discarded, `matchChord` sees a ctrl `^`/`6` follow-up and runs `chordLiteral` instead of ignoring the release (`cmd/hoot/attach_fsm.go:207-260`, especially `cmd/hoot/attach_fsm.go:244-247`).
- The FSM resets to normal (`cmd/hoot/attach_fsm.go:295-316`).
- The following `.` is forwarded as ordinary input. Detach never fires.

iTerm still works because it does not fully honor this kitty keyboard mode stack, so hoot continues to receive the legacy byte stream `0x1e 0x2e`.

## Recommendation

Make CSI u parsing event-aware.

- Add an `event uint` field to `keyEvent`.
- Change `parseCSIuParams` to return `(cp, altCp, mod, event)`, where event is:
  - `0` when the terminal omitted event type,
  - `1` for press,
  - `2` for repeat,
  - `3` for release.
- Treat `event == 0` and `event == 1` as press-like for the existing matcher.
- Do not match `event == 2` or `event == 3` as a prefix or chord command.
- In `stateAfterPref`, silently ignore CSI u repeat/release events and remain in `stateAfterPref`; this lets the real follow-up `.` detach after the prefix key is released.
- A press of any non-chord key after the prefix should keep the existing behavior: consume the follow-up and reset to normal.
- In `stateNormal`, forward non-prefix release/repeat events verbatim. The special swallowed case is only release/repeat while hoot is already holding an internal prefix chord window.

This policy keeps the detach chord press-driven, prevents prefix releases from canceling the chord window, and avoids stealing unrelated release events from the attached program.

## Steps

- Add event parsing to `parseCSIuParams` and `keyEvent`.
- Add a small helper such as `isCSIuPress(ev)` or `eventKind` constants for `press`, `repeat`, and `release`.
- Require press-like events in `isPrefixKey`.
- Ignore CSI u repeat/release events in `stateAfterPref` before calling `matchChord`.
- Add unit tests for the Codex/Ghostty/kitty event stream: prefix press, prefix release, legacy `.` detaches.
- Add unit tests for explicit press tags (`;6:1`) and release/repeat variants (`;6:2`, `;6:3`) so repeat/release do not trigger prefix/chord commands.
- Add a regression test for release before the wrong follow-up, proving the release does not cancel the window but the wrong press still does.
- Run focused hoot FSM tests and the full hoot test suite.
- Manually smoke in Ghostty, kitty, and iTerm against Codex.

## Verification

Automated:

- `go test ./cmd/hoot -run 'TestRunChordFSM|TestParseCSIuParams|TestIsPrefixKey' -v`
- `go test ./cmd/hoot -v`
- If the repo has broader required checks, run them after the focused tests.

Manual smoke:

- Start a hoot session with Codex inside: `pnpm dlx @openai/codex --dangerously-bypass-approvals-and-sandbox`.
- Attach from Ghostty and press `C-^`, release it, then press `.`. Expected: hoot detaches.
- Repeat from kitty. Expected: hoot detaches.
- Repeat from iTerm. Expected: hoot detaches via legacy bytes.
- Optional byte capture: run `tmp/capture-kitty-keybytes.sh tmp/<terminal>-keybytes.bin tmp/<terminal>-keybytes.txt` inside the terminal, press `C-^ .`, and preserve the txt file in `## Evidence`.

## Open questions

- Live Ghostty/kitty byte capture was blocked by macOS GUI automation permissions. Should implementation proceed from source-derived traces, or should the boss/human provide live captures before implementation?

## Design notes

- 2026-05-07T13:18Z - The initial hypothesis included possible report-all behavior, but Codex only pushes flags `7`, not `15` or `31`.
  - That changes the failure from "the `.` follow-up is CSI u" to "the prefix release consumes the chord slot".
  - The existing `CSI 46 u` follow-up support remains useful for other programs that request report-all, but Codex does not appear to be doing that today.
  - I still recommend adding tests for `CSI 46;1:1 u` as a press-tag follow-up because it falls out of the same event parser and protects future report-all mode users.

- 2026-05-07T13:18Z - Picked "ignore release/repeat only while after-prefix" over globally dropping release/repeat events.
  - Global dropping would be simpler but would corrupt input for inner programs that intentionally consume key releases.
  - Treating release/repeat as ordinary non-prefix events in normal state preserves proxy behavior.
  - Swallowing release/repeat after an already-consumed prefix is consistent with hoot's existing prefix handling: once the prefix starts an internal chord, those bytes belong to hoot's chord recognizer, not the inner TUI.
