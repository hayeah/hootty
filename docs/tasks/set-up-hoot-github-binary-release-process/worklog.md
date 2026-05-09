---
status: done
section: Set up hoot GitHub binary release process
slug: set-up-hoot-github-binary-release-process
mode: worktree
spec: spec.md
created: 2026-05-08T10:05:43Z
---

> ## Set up hoot GitHub binary release process
>
> ---
> status:
>   type: open
> ---
>
> Stand up a release pipeline for hoot. Semantic versioning starting at `v0.0.1` (no stability guarantee yet); git tag triggers release; build linux + macOS for both common architectures.
>
> Tool choice is open — GoReleaser is the obvious candidate; the spec should weigh alternatives and pick one.
>
> Goldilocks goal: a remote-helper install path that's painless to bootstrap on an SSH host. The human's reference design for this UX is:
>
>   `/Users/me/Dropbox/notes/2026-05-06/zed-vscode-ssh-remote-helper_claude.md`
>
> Read that note first. The spec should call out which parts apply to hoot, which don't, and propose a concrete install path (one-line curl-pipe? a `hoot install-remote <host>` verb? both?). The `hoot @host` flow should compose with this — SSHing to a fresh box and getting hoot bootstrapped automatically would be the ideal end state.
>
> In scope: release tool choice, GitHub Actions workflow, archive/checksum layout, install scripts, the remote-bootstrap path. Don't over-engineer signing in v0.0.1 — checksum + tag is enough; full code-signing can be a follow-up.
>
> - [ ] rfc: review spec.md
> - [ ] implement per spec

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### Phase 0 — spec (current; gated on rfc tick)

- [x] Read the zed/vscode reference note.
- [x] Inspect hootty repo (build system, `hoot @host` ssh transport, cross-compile recipe).
- [x] Draft `spec.md`.
- [x] Wait for `rfc: review spec.md` to be ticked, addressing any feedback in `## Boss log`.

### Phase 1 — release infrastructure (after rfc)

- [x] Add `cmd/hoot/version.go` + `version` subcommand wired into `main.go` dispatch.
- [x] Bootstrap `Makefile.py` with `ghostty:fetch` + `libghostty:[T]` tasks. Verify musl assumption locally on all 3 targets.
- [x] Add `hoot:[T]` + `tarball:[T]` tasks. Confirm `ldd hoot-linux-amd64/hoot` → "not a dynamic executable".
- [x] Add `checksums` + phony `release` task with preconditions (clean tree, tag check, gh auth).
- [x] Push throwaway `v0.0.0-rc1` tag, iterate until green.

### Phase 2 — install paths

- [x] Write POSIX `install.sh` (uname-based platform detection, checksum verify).
- [x] Add `internal/bootstrap` package: probe-then-install, three modes (fetch-on-remote / upload-from-local / from-file).
- [x] Add `cmd/hoot/install_remote.go` calling into bootstrap.
- [x] Wire auto-bootstrap into `internal/sshtransport` so `hoot @host` self-installs hoot when missing.
- [x] Smoke test the three install flows on `devbox`.
- [x] Update `README.md`; add `docs/install.md` and `docs/releasing.md`.

### Phase 3 — ship (user-driven)

- [ ] Tag `v0.0.1` for real. **User-driven** per the earlier rc-publish decision: "You confirm v0.0.1 yourself separately." The pipeline is fully verified end-to-end via three independent rc1 publish/teardown cycles. To ship v0.0.1: `git tag v0.0.1 && git push origin v0.0.1 && HOOT_VERSION=v0.0.1 pymake release`. (Drop `--prerelease` from `Makefile.py` once we cross to `0.1.x` — see `docs/releasing.md`.)

## Agent log
- 2026-05-08T17:10Z — Spawned. Read AGENT_LOOP.md and the zed/vscode SSH-helper reference note. Confirmed repo is `hootty` (binary `hoot`), cgo-via-Zig build via `docs/cross-compile.md`. Created worktree `set-up-hoot-github-binary-release-process` and symlinked under `repos/github.com/hayeah/hootty`. Drafted `spec.md` covering tool-choice rationale (plain GH Actions over GoReleaser; reasoning recorded), 4-target build matrix, archive layout, `install.sh` curl-pipe, `hoot install-remote` verb, and `hoot @host` auto-bootstrap. Section's first checkbox is `rfc: review spec.md` — set `status: blocked` and stopping for review.
- 2026-05-08T10:10Z spec.md drafted; status: blocked awaiting rfc tick
- 2026-05-08T10:18Z Revised spec: switched from GH Actions to local release script (scripts/release.sh + Makefile target). Added rationale + trade-offs in 'Tool choice'. Pinned glibc floor at 2.28 explicitly via 'zig cc -target …gnu.2.28'. Documented glibc dynamic-linking implications in Background.
- 2026-05-08T10:22Z Switched Linux to musl-static (zig -target x86_64/aarch64-linux-musl). Removed glibc-floor open question. Maintainer's libghostty-glibc-free observation was the trigger; recorded full alternatives weighing in design notes.
- 2026-05-08T10:27Z Switched release tooling from flat shell scripts to pymake Makefile.py. Build dep graph: ghostty:fetch → libghostty:[T] → hoot:[T] → tarball:[T] → checksums → release (phony). tree_digest gates Go rebuilds on real source change. Phase 1 todos updated to match.
- 2026-05-08T10:30Z [54abcb8] Added cmd/hoot/version.go + version subcommand. Default Version="dev"; ldflags injection verified ('go build -ldflags -X main.Version=v0.0.1-test' → 'hoot version' prints 'v0.0.1-test'). Dispatch + version tests pass.
- 2026-05-08T10:35Z [d8f729d] Bootstrapped Makefile.py with ghostty:fetch + libghostty:[T] tasks. Musl assumption VERIFIED: -Dtarget=x86_64-linux-musl and aarch64-linux-musl both accepted by Ghostty's build.zig (no fallback to gnu.2.28 needed). All three libghostty-vt.a archives produced (darwin: 7.5MB Mach-O; linux x2: 13MB ar with GNU-style index).
- 2026-05-08T10:41Z [5b4353a] Added hoot:[T] + tarball:[T]. All 3 targets build statically: file dist/hoot-linux-amd64/hoot reports 'statically linked, ELF 64-bit LSB x86-64'; same for arm64. Tarballs produced. #friction tree_digest.changed doesn't compose with per-target task.register loops (auto-commit-after-body means only the first consumer sees changed=True); switched to explicit Go-source glob — see Trouble report.
- 2026-05-08T10:43Z [3996697] Added checksums + release tasks. Preconditions enforced (HOOT_VERSION set, clean tree, tag matches HEAD, release doesn't exist, gh auth). HOOT_VERSION env var threads tag through hoot:[T] -ldflags. Tested negative paths (no HOOT_VERSION → clear error; dirty tree → 'uncommitted changes'). Pausing before next todo (publish rc1 tag) — that's an irreversible-ish public action; awaiting confirmation.
- 2026-05-09T07:29Z Created public repo hayeah/hootty, pushed master + feature branch, drove rc1 dry run end-to-end. Caught + fixed [16582e5]: precondition needed 'git describe --tags --exact-match' to accept lightweight tags (default of 'git tag <name>'). Release task built all 3 targets, computed checksums, uploaded to https://github.com/hayeah/hootty/releases/tag/v0.0.0-rc1. Verified: tarball SHA matches checksums.txt; extracted darwin binary's 'hoot version' prints 'v0.0.0-rc1'. Cleaned up: release + remote tag + local tag deleted. Reordered todos: v0.0.1 moves to Phase 3 (after install paths), since the spec's v0.0.1 smoke tests rely on install.sh / hoot install-remote / @host auto-bootstrap.
- 2026-05-09T07:33Z [426dad0] Added install.sh (POSIX, no jq). Validated end-to-end: republished rc1, ran 'cat install.sh | sh -s -- --version v0.0.0-rc1' → installed binary printing 'v0.0.0-rc1'. Verified bad-checksum path refuses to install ('shasum: WARNING: 1 computed checksum did NOT match' → exit nonzero). Latest-release auto-detect 404s on a prerelease-only repo (correct GH API behavior — will work once v0.0.1 ships). rc1 cleaned up.
- 2026-05-09T07:39Z [bootstrap pkg] Added internal/bootstrap with Probe + Install (3 modes: Auto/Upload/FromFile). install.sh embedded via //go:embed; lives in 2 places (repo root for curl-pipe, package dir for embed) with a sync test + release-time precondition. Added install.sh --from-file flag for Upload/FromFile modes. Tests pass: TestInstallShStaysInSync, TestInstallScriptEmbedded, TestSshArgs (4 cases), TestShellQuote (5 cases), TestModeString, TestVerifySHA. Commits: 426dad0 install.sh, [bootstrap commit], 3f80cda sync-check precondition.
- 2026-05-09T07:42Z Added cmd/hoot/install-remote subcommand. Default Auto mode (remote downloads), --upload (local-fetch + scp + install), --from-file (caller-supplied tarball + scp + install). Default version pins to compiled-in Version (Zed-style). 'hoot install-remote -h' renders cleanly; bad-args paths surface clear errors. parseSSHTarget tests pass for {host, user@host, host:port, user@host:port, ssh://..., scheme rejection}.
- 2026-05-09T08:24Z Phase 2 smoke-tested on devbox (Linux x86_64). All 4 flows pass: install-remote default/Auto, --upload, --from-file, and 'hoot list --remote ssh://devbox' clean-state auto-bootstrap. 2 issues caught: stdlib-flag-order gotcha (#friction; documented), and macOS tar Apple-xattr warnings on Linux extract (fixed in dcd97e8 via --no-mac-metadata). rc1 cleaned up.
- 2026-05-09T08:26Z Phase 2 complete. README has Install section + updated CLI synopsis (install-remote, version). docs/install.md covers 3 modes + auto-bootstrap + HOOT_NO_BOOTSTRAP opt-out + flag-order gotcha. docs/releasing.md walks through tag→pymake release flow, preconditions, dry-run pattern, failed-publish recovery, Ghostty SHA bump procedure. Ready for Phase 3 publish (v0.0.1).
- 2026-05-09T08:28Z All my todos done. status: done. v0.0.1 is user-driven per earlier publish-flow decision. Branch pushed to origin/set-up-hoot-github-binary-release-process (12 commits). Pipeline verified end-to-end via two rc1 publish cycles + 4 install-flow smoke tests on devbox. Updated spec.md design notes with three load-bearing implementation discoveries (install.sh dual-copy + sync test, HOOT_VERSION env-var threading, tree_digest multi-consumer foot-gun). Spec open questions all resolved. Ready for boss review.

## Boss log
- 2026-05-08T10:28Z ticked: rfc

## Evidence

### Phase 1 — release pipeline (verified via v0.0.0-rc1 dry run, then deleted)

**Pipeline shape** (commits on `set-up-hoot-github-binary-release-process` branch):
- `54abcb8` cmd/hoot/version.go + version subcommand
- `d8f729d` Makefile.py with ghostty:fetch + libghostty:[T]
- `5b4353a` hoot:[T] + tarball:[T]
- `3996697` checksums + release task with preconditions
- `16582e5` Fix: --tags flag for lightweight-tag support

**rc1 publish transcript (truncated)**:

```
$ HOOT_VERSION=v0.0.0-rc1 pymake release
[skip] ghostty_fetch (up to date)
[run] libghostty:darwin-arm64
[run] hoot:darwin-arm64
[run] tarball:darwin-arm64
[run] libghostty:linux-amd64
[run] hoot:linux-amd64
[run] tarball:linux-amd64
[run] libghostty:linux-arm64
[run] hoot:linux-arm64
[run] tarball:linux-arm64
[run] tarballs
[run] checksums
https://github.com/hayeah/hootty/releases/tag/v0.0.0-rc1
[run] release
```

**Asset list at the GitHub release**:

```
checksums.txt           271 bytes
hoot-darwin-arm64.tar.gz 3,499,022 bytes
hoot-linux-amd64.tar.gz  5,700,494 bytes
hoot-linux-arm64.tar.gz  5,224,477 bytes
```

**Static-linkage invariant (the load-bearing musl assumption)**:

```
$ file dist/hoot-linux-amd64/hoot
ELF 64-bit LSB executable, x86-64, statically linked, ...
$ file dist/hoot-linux-arm64/hoot
ELF 64-bit LSB executable, ARM aarch64, statically linked, ...
```

**End-to-end roundtrip (download published asset, verify checksum + version)**:

```
$ gh release download v0.0.0-rc1 -p 'hoot-darwin-arm64.tar.gz' -p 'checksums.txt'
$ shasum -a 256 -c checksums.txt | grep darwin-arm64
hoot-darwin-arm64.tar.gz: OK
$ tar -xzf hoot-darwin-arm64.tar.gz && ./hoot version
v0.0.0-rc1
```

**Cleanup confirmed**: release deleted, remote tag deleted, local tag deleted (`gh release view v0.0.0-rc1` → "release not found"; `git ls-remote origin refs/tags/v0.0.0-rc1` → empty).

### Phase 2 — install paths (smoke-tested on devbox / Linux x86_64, then cleaned up)

Re-published `v0.0.0-rc1` against the Phase 2 commits, ran four smoke tests against `devbox`. Local CLI was `dist/hoot-darwin-arm64/hoot` (Version=v0.0.0-rc1 baked in via -ldflags).

**Test 1 — `hoot install-remote devbox` (default = ModeAuto, remote downloads from GitHub)**

```
$ hoot install-remote devbox
install.sh: target=linux-amd64 version=v0.0.0-rc1
install.sh: downloading https://github.com/hayeah/hootty/releases/download/v0.0.0-rc1/hoot-linux-amd64.tar.gz
install.sh: installed v0.0.0-rc1 to /home/me/.local/bin/hoot
$ ssh devbox '~/.local/bin/hoot version'
v0.0.0-rc1
```

**Test 2 — `hoot install-remote --upload devbox` (ModeUpload: local fetch + checksum + scp + install)**

```
$ hoot install-remote --upload devbox
bootstrap: downloading https://github.com/hayeah/hootty/releases/download/v0.0.0-rc1/hoot-linux-amd64.tar.gz
bootstrap: downloading https://github.com/hayeah/hootty/releases/download/v0.0.0-rc1/checksums.txt
install.sh: target=linux-amd64 using --from-file=/tmp/hoot-install-54012.tar.gz
install.sh: installed from-file to /home/me/.local/bin/hoot
$ ssh devbox '~/.local/bin/hoot version'
v0.0.0-rc1
```

**Test 3 — `hoot install-remote --from-file dist/hoot-linux-amd64.tar.gz devbox` (ModeFromFile)**

```
$ hoot install-remote --from-file dist/hoot-linux-amd64.tar.gz devbox
install.sh: target=linux-amd64 using --from-file=/tmp/hoot-install-54227.tar.gz
install.sh: installed from-file to /home/me/.local/bin/hoot
$ ssh devbox '~/.local/bin/hoot version'
v0.0.0-rc1
```

**Test 4 — `hoot list --remote ssh://devbox` from a clean state (auto-bootstrap via newSSHRemote)**

```
$ ssh devbox 'rm -f ~/.local/bin/hoot'    # start from missing
$ hoot list --remote ssh://devbox
hoot: remote has <missing>, installing v0.0.0-rc1
install.sh: target=linux-amd64 version=v0.0.0-rc1
install.sh: downloading ...
install.sh: installed v0.0.0-rc1 to /home/me/.local/bin/hoot
                                          # then `list` runs (no sessions; no output)
$ hoot list --remote ssh://devbox         # second run: probe matches, no install path
$                                          # silent + exit 0
```

All four flows passed. Caught two issues during the run, fixed in this branch:

- `flag` stdlib doesn't accept flags after positionals; user-visible as "expected exactly one <host> argument (got 2)". Documented in trouble report; `--upload`/`--from-file` must come before the host. Not invasive enough to swap to a third-party flag lib.
- macOS-built tarballs warned on Linux extract (`tar: Ignoring unknown extended header keyword 'LIBARCHIVE.xattr.com.apple.provenance'`). Fixed in `dcd97e8` by passing `--no-mac-metadata` to the tarball:[T] task.

Cleanup: `~/.local/bin/hoot` removed on devbox; rc1 release + tag deleted.

## Trouble report

- 2026-05-09 — `hoot install-remote devbox --upload` fails with "expected exactly one <host> argument (got 2)" because Go's stdlib `flag` package stops parsing at the first non-flag argument; flags must come before positional. Fix is documented in `--help` (host appears at the end of the synopsis) but the error message could be friendlier. Workaround: `hoot install-remote --upload devbox`. Considered switching to a lib that allows interleaved flags but stdlib flag is what every other hoot subcommand uses; cross-the-board change is out of scope. #friction
- 2026-05-09 — macOS BSD `tar` embeds Apple xattrs (`com.apple.provenance`) into release tarballs. GNU tar on Linux warns "Ignoring unknown extended header keyword" on every file during install — cosmetic but loud. Fixed in `dcd97e8` with `tar --no-mac-metadata` in the tarball:[T] task.
- 2026-05-08 — `tree_digest.changed` does NOT compose with `task.register` in a per-target loop. The first task that runs commits the digest at body-end; subsequent tasks see "changed=False" and skip even when their outputs are missing. Caught when `pymake hoot:linux-amd64` reported "[skip] hoot:linux-amd64 (run_if returned False)" right after `pymake hoot:darwin-arm64` finished, with no linux binary produced. Worked around by enumerating `cmd/**/*.go + internal/**/*.go + go.mod + go.sum` as explicit inputs. Pymake docs warn about a related pitfall (output-dir-in-digest) but not this multi-consumer case. #friction
