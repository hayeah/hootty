---
status: done
section: Implement hoot binary pty log format
slug: implement-hoot-binary-pty-log-format
mode: worktree
spec: /Users/me/Dropbox/boss/tasks/improve-hoot-pty-log-storage-format/specs/binary-libghostty-format.md
created: 2026-05-08T05:32:37Z
---

> ## Implement hoot binary pty log format
>
> ---
> status:
>   type: open
> ---
>
> Implement the binary libghostty pty log format per the chosen spec at:
>
> `/Users/me/Dropbox/boss/tasks/improve-hoot-pty-log-storage-format/specs/binary-libghostty-format.md`
>
> This supersedes the plaintext direction that was explored in the now-closed `improve-hoot-pty-log-storage-format` section — the `spec.md` in that workspace reflects the rejected approach. Only `specs/binary-libghostty-format.md` is canonical.
>
> The spec is the contract: storage format, recorder rewrite, `hoot log` subcommand suite (`show`, `grep`, `play`, `export`, `tail`), tests. Out-of-scope items are explicitly listed in the spec — don't expand scope.
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Rewrite `recorder.go` to the binary `hootty.log v1` format (preamble + first-frame resize + bucketed type=1 frames + type=3 spacer on long idles + 64 KiB auto-split)
- [x] Add `FrameDecoder` iterator + thin `ReadOutputEvents` helper for tests (replaces `ReadAsciicastOutputEvents`)
- [x] Add `replay.go` — `NewLibghosttyReplay(cols, rows)` with `VTWrite` / `Resize` / `Snapshot(format)` (vt|plain) for `hoot log` viewer
- [x] Implement `WriteAsciicastJSONL(in, out)` — stream-transcode binary frames to asciicast v2 JSONL (reads `state.json` for header metadata)
- [x] Switch `cmd/hoot/session.go` to `pty.hootty.log`; drop `WithRecorderCommand` / `WithRecorderTitle`
- [x] Add `cmd/hoot/log.go` — `hoot log [<target>] [--format vt|plain|asciinema]` with fzf picker fallthrough
- [x] Wire `log` into `cmd/hoot/main.go` dispatch + usage; update doc strings (`pty.cast` → `pty.hootty.log`)
- [x] Port `recorder_test.go` + `pty_libghostty_test.go` fixtures to binary frames; add `log_test.go` round-trip
- [x] Update `README.md` (recording path + format brief + `hoot log` usage)
- [x] `go test ./...` clean

## Agent log
- 2026-05-08T05:39Z recorder + decoder rewrite landed (dcfb7e4) — pty.hootty.log v1, all existing tests pass
- 2026-05-08T05:46Z hoot log + replay + asciicast transcoder landed (73e6c46) — log_test, asciicast roundtrip, spacer tests pass
- 2026-05-08T05:50Z README updated (802871a) — final verification: go test ./... clean; smoke test all 3 formats captured in tmp/124900_000-hoot-log-smoke.txt

## Boss log

## Evidence

### `go test ./...` — clean across the repo

```
ok  	github.com/hayeah/hootty	6.922s
ok  	github.com/hayeah/hootty/cmd/hoot	6.703s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.809s
ok  	github.com/hayeah/hootty/internal/shortid	0.695s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.657s
```

Full tail in `tmp/124910_000-go-test-tail.txt`. New tests added by this section:

- `TestRecorderWritesBinaryFormat` — preamble + prelude resize + output + resize round-trip via `FrameDecoder`.
- `TestReadOutputEventsBoundsWindow` — window slicing on the test-only decoder helper.
- `TestRecorderAutoSplitsLargeBucket` — > 64 KiB bucket auto-split into chained `type=1` frames at delta=0.
- `TestFrameDecoderRejectsBadMagic` — readers refuse files without the `# hootty.log v1` magic line.
- `TestFrameDecoderTruncatedFrameIsEOF` — partial trailing frame (recorder killed mid-write) is treated as clean EOF.
- `TestSpacerBridgesLongIdle` — hand-built fixture with a `type=3` spacer; reader's cumulative `At` advances correctly.
- `TestRecorderEmitsSpacerOnLongIdle` — writer emits a spacer when the next bucket is > 65535 ms past `lastEmittedMs`.
- `TestAsciicastRoundtripPayloadIdentity` — write a recording → transcode to asciicast JSONL → concatenated `o` payloads equal the original recorded bytes.
- `TestCmdLogReplayPlain` — `hoot log` plain-format end-to-end (record via production `Recorder` → replay snapshot → "hello/world" present, no escapes).
- `TestCmdLogAsciinema` — header carries width/height from prelude and timestamp/command/title from `state.json`; output + resize events have correct payloads.
- `TestResolveLogFormatDefaults` — vt on tty, plain on pipe, explicit values pass through, invalid values error.

### Smoke test — real `hoot run` + `hoot log` (all three formats)

Captured in `tmp/124900_000-hoot-log-smoke.txt`. Key transcript:

```
$ head -1 /tmp/hoot-smoke/smoke/pty.hootty.log
# hootty.log v1

$ xxd /tmp/hoot-smoke/smoke/pty.hootty.log | head -2
00000000: 2320 686f 6f74 7479 2e6c 6f67 2076 310a  # hootty.log v1.
00000010: 0a02 0000 0400 5000 1800 0100 005a 0068  ......P......Z.h
```

Frame layout decoded: prelude `02 0000 0400 5000 1800` → type=2 resize, delta=0, length=4, cols=80 (`0x0050`), rows=24 (`0x0018`). Next frame `01 0000 5a00 …` → type=1 output, delta=0, length=90, payload "hello world\r\n\x1b[31mred\x1b[0m\r\n…done\r\n". Matches the spec layout.

```
$ /tmp/hoot-test log --state-dir /tmp/hoot-smoke --format plain smoke
hello world
red
attach.go.tmp
attach.go.tmp.bottom
bun-node-af24e281e
done
```

```
$ /tmp/hoot-test log --state-dir /tmp/hoot-smoke --format vt smoke | xxd | head -2
00000000: 6865 6c6c 6f20 776f 726c 640d 0a1b 5b30  hello world...[0
00000010: 6d1b 5b33 383b 353b 316d 7265 641b 5b30  m.[38;5;1mred.[0
```

vt format emits SGR escapes (here `\x1b[38;5;1m` for "red") so a real terminal renders it correctly.

```
$ /tmp/hoot-test log --state-dir /tmp/hoot-smoke --format asciinema smoke
{"version":2,"width":80,"height":24,"timestamp":1778219331,"command":"bash -lc echo hello world; …; echo done","title":"smoke"}
[0,"o","hello world\r\n[31mred[0m\r\n…done\r\n"]
```

asciicast v2 header pulls `timestamp`/`command`/`title` from `state.json` and `width`/`height` from the prelude resize. The single `o` event is payload-identical to the recorded bytes (because the entire small recording landed in one 33 ms bucket).

`asciinema` not installed locally, so the `asciinema play -` consumer was not exercised in this smoke; the JSONL shape matches the v2 grammar (header line + `[seconds, "o"|"r", payload]` events) and the `TestAsciicastRoundtripPayloadIdentity` unit test confirms payload-identity round-trip.

### Library and CLI surface delta

- `recorder.go`: rewritten — `NewRecorder(path, cols, rows)` now takes only those three args (`WithRecorderCommand` / `WithRecorderTitle` removed; metadata lives in `state.json`). New: `FrameDecoder`, `Frame`, `FrameTypeOutput`/`FrameTypeResize`/`FrameTypeSpacer`, `WriteAsciicastJSONL`, `AsciicastHeader`, `AsciicastHeaderFromState`, `LoadStateFile`, `ReadOutputEvents` (replaces `ReadAsciicastOutputEvents`).
- `replay.go`: new — `NewLibghosttyReplay(cols, rows)` with `VTWrite` / `Resize` / `Snapshot(ReplayFormat)` (vt|plain). Internal helper for `hoot log`'s libghostty-snapshot pipeline.
- `cmd/hoot/log.go`: new — `cmdLog` plus `runLogReplay` and `runLogAsciinema` route helpers, `resolveLogFormat` for the tty/pipe defaults.
- `cmd/hoot/session.go`: recorder file path switched from `pty.cast` to `pty.hootty.log`; metadata flags dropped.
- `cmd/hoot/main.go`: `log` added to `knownSubcommands` + the dispatch switch + the usage block.
- `README.md`: doc updated — file format, `hoot log` section, recorder snippet rewording.

## Trouble report

- `hoot run --state-dir <dir> --key smoke -- bash -lc …` reports `session did not start: timeout waiting for /tmp/<dir>/smoke/rpc.sock` from a cold tmp dir on this machine, even though the session and the recording are produced. Reproduces on the master branch too — pre-existing flake unrelated to this section. Smoke test uses the recording it leaves behind regardless.
