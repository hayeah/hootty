---
title: hoot GitHub binary release process
slug: set-up-hoot-github-binary-release-process
status: draft
---

# hoot GitHub binary release process

## Goal

Stand up a tag-driven binary release pipeline for `hoot`
(`github.com/hayeah/hootty`, binary `hoot`) starting at `v0.0.1`. Each
release produces GitHub-release artifacts for
`darwin-arm64`, `linux-amd64`, `linux-arm64` plus a `checksums.txt`,
**built locally** from a single macOS host (the maintainer's laptop)
and uploaded via `gh release create`. No GitHub Actions. On top of that,
ship two install paths so a fresh SSH host can be bootstrapped painlessly:

- A vendor-hosted `install.sh` (one-line curl-pipe).
- A local `hoot install-remote <host>` verb that pushes the matching-version
  binary onto a remote.

The `hoot @host` flow must compose with these: SSHing to a fresh box and
getting `hoot serve` running automatically is the end state. Code signing,
notarization, and homebrew taps are explicitly **out of scope** for v0.0.1
— a SHA-256 sum file and the immutable git tag are the integrity story.

Out of scope for v0.0.1 (called out so the spec doesn't grow):

- **GitHub Actions / any CI.** The release script runs locally on the
  maintainer's macOS laptop. Adding a CI on-tag mirror is a follow-up
  once contributors join or the build needs reproducibility-by-multiple-
  builders.
- macOS `codesign` / notarization (binary will trigger Gatekeeper on
  download — acceptable; user runs `xattr -d com.apple.quarantine`).
- Linux package formats (deb/rpm), Homebrew tap, AUR, nix flake outputs.
- Windows. (`hoot` already depends on libghostty-vt with POSIX-style
  cgo plumbing; no Windows port exists.)
- Sigstore / cosign / SLSA provenance. Defer to a follow-up release once
  the tag → release loop has shaken out.
- Multi-version coexistence on a remote (Zed-style
  `hoot-{channel}-{version}` filenames). Single `~/.local/bin/hoot` for
  v0.0.1; revisit once we have more than one client speaking to a host.

## Background — why this is non-trivial

Building `hoot` is **not** "GOOS=linux go build". It is a cgo build that
needs `libghostty-vt` (a Zig-built static archive) reachable via
`pkg-config` at link time, and Zig as the C compiler when
cross-compiling. The current `docs/cross-compile.md` recipe is what CI
must reproduce. Concretely, a build of any target needs:

- Go 1.26
- Zig 0.15.2 (Zig 0.16 does **not** work — pinned by Ghostty itself)
- CMake + pkg-config
- Ghostty source at the commit pinned in
  `mitchellh/go-libghostty`'s `CMakeLists.txt`
  (currently `9e080c5a403475dcbee93c40eeb22cf6f92121f4`)
- `go-libghostty`'s `make build` (or equivalent CMake invocation) to
  produce `build/_deps/ghostty-src/zig-out/share/pkgconfig/`
- `PKG_CONFIG_PATH` pointing at that pkgconfig dir
- `CGO_ENABLED=1`, plus when cross-compiling: `CC="zig cc -target <triple>"`
- `go build -o bin/hoot ./cmd/hoot`

Because `go-libghostty` is currently `replace`d to a local checkout in
`go.work` (gitignored), the release build also needs to ensure
`mitchellh/go-libghostty` is at the same commit `go.mod` requires
(currently `v0.0.0-20260428141358-29fdb3130d7c`) — independent of any
local `replace`. The release script runs `go build` from a clean tree
with `GOFLAGS=-mod=mod` (or with `go.work` temporarily moved aside) so
the resolved version comes from `go.mod`, not the local checkout.

### libc story on Linux: musl-static

Two separate things to keep straight:

- **`libghostty-vt` itself is libc-free.** It's a Zig static archive
  (`libghostty-vt.a`) with empty `Libs.private` / `Requires.private` in
  its pkgconfig — the archive declares no transitive C dep, and Zig
  produces its own runtime when built with `-Dapp-runtime=none`. So
  Ghostty's VT engine adds *no* libc requirement on its own.
- **cgo itself drags in a libc.** With `CGO_ENABLED=1`, Go's
  `runtime/cgo` glue requires `malloc`, `pthread_*`, `errno`, signal
  forwarding etc. from a libc. We must call libghostty-vt from Go via
  cgo, so we must have *some* libc. Question is which.

**Decision: link statically against musl.** Cross via
`zig cc -target {x86_64,aarch64}-linux-musl`. The resulting Linux
binaries are **fully static** (no `.so` deps; `ldd` reports "not a
dynamic executable"). They run on any Linux: Ubuntu, Alpine, distroless
containers, RHEL 7, embedded boxes. No glibc floor to document, no
`gcompat` workaround.

Why not glibc-dynamic (the original draft):

- Glibc-dynamic forces a glibc-floor decision (we picked 2.28). Anything
  older won't run. Mostly fine, but a needless paper cut.
- Won't run on Alpine without `gcompat`; won't run in distroless. Niche
  today, but constrains future deployments.
- The "fewer surprises" property of a single static binary that *just
  runs everywhere* is meaningful for a CLI that gets sshed onto
  arbitrary hosts.

Trade-offs accepted with musl:

- Slightly larger binary (~+500KB–1MB for the bundled musl). Irrelevant
  for a CLI.
- Go cgo + musl historically had rough edges (DNS via nsswitch on glibc
  vs `/etc/resolv.conf` directly on musl; locale tables). For hoot:
  no DNS lookups happen inside the binary (openssh handles ssh
  resolution, internal connections are unix sockets / known IPs), and
  nothing is locale-sensitive. Should be quiet.
- **Unverified**: whether Ghostty's `build.zig` (with
  `-Demit-lib-vt=true -Dapp-runtime=none`) builds cleanly for
  `*-linux-musl`. Zig handles musl natively, and the VT-only target is
  small, but Ghostty might have a glibc-ism. Verify in Phase 1 before
  committing — if it breaks, fall back to `gnu.2.28` and revisit.

macOS uses `libSystem` (Apple's libc) — not relevant to this discussion.
Native `go build` on macOS Just Works.

## Architecture

### Repository layout (new files)

```
Makefile.py              # pymake build graph: ghostty src → libghostty.a per
                         #   target → hoot binary per target → tarballs →
                         #   checksums → release
install.sh               # vendor-hosted curl-pipe installer
cmd/hoot/
  install_remote.go      # `hoot install-remote <host>` subcommand
  version.go             # exposes Version string (set via -ldflags)
internal/
  bootstrap/             # shared install-on-remote logic (used by both
    bootstrap.go         #   `install-remote` and `hoot @host` auto-bootstrap)
docs/
  install.md             # user-facing install doc
  releasing.md           # how-to: cut a release
```

The existing top-level `Makefile` stays as-is for the dev `build` / `test`
targets; pymake handles release-only workflows in a separate `Makefile.py`.

### Versioning

- Pure semver, prefixed `v`. v0.0.1 is the first tag.
- `cmd/hoot/version.go` exports `var Version = "dev"`. Release builds
  inject the tag via `go build -ldflags "-X main.Version=v0.0.1"`.
- A `hoot version` subcommand already exists in spirit (it's how the
  ssh transport probes remote presence — `hoot version` exits 0 if the
  binary is runnable). Add an explicit `version` subcommand that prints
  the embedded version (one line, no decoration), so version-string
  comparison from a remote is a one-shot RPC. (See `docs/cross-compile.md`
  smoke commands: it already invokes `hoot --help` to test runnability.)

### Tool choice — pymake locally vs. GoReleaser vs. GitHub Actions

**Decision: a local pymake `Makefile.py` driven from the maintainer's
macOS host.** All three targets are produced from one host using the
existing toolchain:

- `darwin-arm64` — native `go build` (system clang as CC).
- `linux-amd64` — cross via `CC="zig cc -target x86_64-linux-musl"`, fully static.
- `linux-arm64` — cross via `CC="zig cc -target aarch64-linux-musl"`, fully static.

This is the same recipe `docs/cross-compile.md` already validates for
linux-amd64 (with the target swapped from `gnu` to `musl`); adding the
arm64 triple is one extra task. Upload happens via
`gh release create $TAG dist/*.tar.gz dist/checksums.txt` from a phony
pymake task.

Why local over GitHub Actions:

- The build's hard parts (Zig 0.15.2 + Ghostty source at a pinned commit
  + libghostty-vt build + pkg-config) all already live on the
  maintainer's laptop and *just work*. CI would have to reproduce that
  every release — installing Zig, cloning Ghostty, building libghostty-vt
  — for no qualitative gain. Local builds skip the entire "make CI match
  my laptop" project.
- Single releaser, private repo. Reproducibility-by-multiple-builders
  isn't a goal yet. A future contributor can add CI when they need it.
- No on-tag trigger needed because the release is a manual act anyway:
  `git tag v0.0.1 && pymake release VERSION=v0.0.1`. The tag and the
  release happen in one breath.

Why pymake (over a flat shell script):

- The build naturally factors into a dep graph: `ghostty-src @ pinned-SHA
  → libghostty-vt[T].a → hoot[T] → hoot-{os}-{arch}.tar.gz → checksums.txt
  → release`. pymake tracks file mtimes so a rebuild loop during Phase 1
  iteration ("rc1 → fix → rc2") doesn't re-clone Ghostty or rebuild
  unchanged libghostty-vt artifacts.
- `tree_digest` over `cmd/` + `internal/` + `go.mod` + `go.sum` gates the
  per-target Go build on real source changes, not "I touched the build
  script".
- Per-target tasks via `task.register` in a loop — cleaner than three
  near-duplicate shell scripts.
- Free `pymake clean --all`, `pymake which release` (visualize plan),
  `pymake -p -j 3` (parallelize the three targets), `pymake redo
  build:linux-amd64 --only` (retry one target).
- Maintainer wrote pymake — high familiarity, low onboarding friction.

Why not GoReleaser:

- GoReleaser's built-in `builds:` block assumes Go-only or a single CC
  override. We need a per-target shell step (build libghostty-vt for
  the target → set PKG_CONFIG_PATH/CC → go build). That is doable via
  `hooks.pre` and `builds[].env`, but at that point GoReleaser is just
  running our shell scripts and giving us archive/checksum conventions
  back.
- The artifact list is small (3 tarballs + 1 checksums.txt). The
  archive convention is one shell line: `tar -czf hoot-{os}-{arch}.tar.gz hoot LICENSE README.md`.
- GoReleaser doesn't add value when the entire pipeline runs on one
  developer's machine and uploads with `gh`.

Trade-offs we're accepting:

- No reproducibility audit trail. If the maintainer's laptop is
  compromised, releases are compromised. Mitigated minimally by
  publishing `checksums.txt` in the GitHub release; full SLSA/sigstore
  is a follow-up.
- No "did the tag still build?" CI signal. A small PR-only sanity
  check (build linux-amd64) could be added cheaply later if regressions
  start sneaking in.
- Single point of failure. If the maintainer's laptop dies mid-release,
  someone else has to set up the toolchain to ship a hotfix. Acceptable
  for v0.0.1; revisit if hoot grows users.

### Build matrix

| Target        | how on macOS host                       | linkage          |
|---------------|-----------------------------------------|------------------|
| darwin-arm64  | native `go build` (system clang)        | dyld + libSystem |
| linux-amd64   | `zig cc -target x86_64-linux-musl`      | static (musl)    |
| linux-arm64   | `zig cc -target aarch64-linux-musl`     | static (musl)    |

**macOS: arm64 only** for v0.0.1. Per-section text says "both common
architectures", but the real audience is the maintainer's own laptops
+ remote Linux dev boxes — all Apple Silicon. Intel Macs aren't a
target. If demand surfaces, adding `darwin-amd64` (zig-cross from arm64,
or rosetta) is one line.

**Linux: fully static via musl.** No glibc floor — the binary contains
its own libc. See the "libc story on Linux" subsection above for full
rationale.

### `Makefile.py` shape

```sh
# default invocation cuts a release for $VERSION
pymake release --vars release.version=v0.0.1
# or set in vars/release.toml and `pymake release`

# dev shortcuts
pymake build                             # all 3 targets, dev/version=v0.0.0-dev
pymake build:darwin-arm64                # one target
pymake -p -j 3 build                     # parallelize
pymake which release                     # visualize the plan
pymake clean --all                       # wipe dist/ + .build/
```

Task graph (one row per pymake task; `[T]` is per-target via
`task.register`):

| task                       | inputs                                                | outputs                              |
|----------------------------|-------------------------------------------------------|--------------------------------------|
| `ghostty:fetch`            | (none — runs once if missing)                         | `.build/ghostty/.fetched-<sha>`      |
| `libghostty:[T]`           | `ghostty:fetch`                                       | `.build/libghostty/[T]/lib/libghostty-vt.a` (+ pkgconfig) |
| `hoot:[T]`                 | `libghostty:[T]`, `tree_digest(cmd, internal, go.mod, go.sum)` | `dist/hoot-[T]/hoot`                 |
| `tarball:[T]`              | `hoot:[T]`, `LICENSE`, `README.md`                    | `dist/hoot-[T].tar.gz`               |
| `checksums`                | all `tarball:[T]`                                     | `dist/checksums.txt`                 |
| `release` (phony)          | `checksums`, `run_if=preconditions`                   | (calls `gh release create`)          |

Preconditions for `release` (phony, fail fast):

1. Working tree is clean (`git diff --quiet && git diff --cached --quiet`).
2. Tag `$VERSION` exists locally and matches `HEAD` (`git describe --exact-match HEAD == $VERSION`). If not, abort with hint to `git tag $VERSION && git push --tags` first.
3. GitHub release for `$VERSION` does **not** already exist (`gh release view $VERSION` returns nonzero).
4. Prereqs: `zig version` reports 0.15.2; `go version` reports 1.26+; `gh auth status` succeeds.
When all preconditions pass, pymake walks the dep graph and runs only
the stale tasks. The `release` task body executes `gh release create
$VERSION --title $VERSION --notes-file dist/release-notes.md
dist/*.tar.gz dist/checksums.txt` (notes file is hand-edited or
auto-generated from `git log $PREV..HEAD --oneline`).

Each individual task (sketches; full `Makefile.py` lives in the repo):

`ghostty:fetch` — clones Ghostty at the SHA pinned in
`mitchellh/go-libghostty/CMakeLists.txt` (currently
`9e080c5a40…`). Reads `mitchellh/go-libghostty`'s commit (per `go.mod`)
to find the right CMakeLists.txt to read the SHA from. Outputs a stamp
file `.build/ghostty/.fetched-<sha>` so re-runs are idempotent.

`libghostty:[T]` — invokes Ghostty's `zig build` directly:

```python
sh(f"""
  cd .build/ghostty/src && \
  zig build \
    -Dtarget={triple} \
    -Dcpu=baseline \
    -Doptimize=ReleaseFast \
    -Dapp-runtime=none \
    -Demit-lib-vt=true \
    --prefix {prefix}
""")
```

where `triple ∈ {aarch64-macos-none, x86_64-linux-musl, aarch64-linux-musl}`
and `prefix = .build/libghostty/[T]`. Outputs the static archive +
pkgconfig.

We invoke `zig build` directly rather than going through the
`go-libghostty` cmake-FetchContent wrapper because the cross-target
plumbing through cmake is more moving parts than benefit, and
`docs/cross-compile.md` already validates this direct path.

`hoot:[T]` — Go build with cgo:

```python
env = {
  "PKG_CONFIG_PATH": f".build/libghostty/{T}/share/pkgconfig",
  "CGO_ENABLED": "1",
  "GOOS": os, "GOARCH": arch,
  "GOFLAGS": "-mod=mod",  # ignore local go.work replace
}
if os == "linux":
  env["CC"] = f"zig cc -target {triple}"
  ldflags = f"-s -w -X main.Version={version} -extldflags=-static"
else:
  ldflags = f"-s -w -X main.Version={version}"
sh(f"go build -trimpath -ldflags '{ldflags}' "
   f"-o dist/hoot-{T}/hoot ./cmd/hoot", env=env)
```

For Linux, `-extldflags=-static` makes the static-musl invariant
explicitly check-able via `ldd dist/hoot-linux-amd64/hoot` →
"not a dynamic executable".

Note on `go.work`: hoot's `go.work` replaces `go-libghostty` to a local
checkout. Setting `GOFLAGS=-mod=mod` (or `GOWORK=off`) on the build env
makes the resolved version come from `go.mod`, not the local replace.
Less invasive than physically moving `go.work` aside.

`tarball:[T]` — stages `dist/hoot-[T]/{hoot,LICENSE,README.md}` and runs
`tar -czf dist/hoot-[T].tar.gz -C dist/hoot-[T] .`.

`checksums` — `cd dist && shasum -a 256 hoot-*.tar.gz > checksums.txt`.

The pre-publish chain (`ghostty:fetch` → `libghostty:[T]` →
`hoot:[T]` → `tarball:[T]` → `checksums`) is idempotent —
re-running after a build failure just rebuilds the stale rung.
`release` is the irreversible "publish" step; if it fails (e.g., `gh`
auth expired) the artifacts are still in `dist/` and `pymake release`
re-runs the publish-only delta after fixing auth.

### Archive / checksum layout

GitHub release at `https://github.com/hayeah/hootty/releases/tag/v0.0.1`:

- `hoot-darwin-arm64.tar.gz`
- `hoot-linux-amd64.tar.gz`
- `hoot-linux-arm64.tar.gz`
- `checksums.txt` (SHA-256, one per line, GNU `shasum -a 256` format)

Note: `darwin-amd64` is intentionally **not** built — see Build matrix
above. `install.sh` should fail with a clear "platform not supported,
v0.0.1 ships darwin-arm64 only" message if `uname -sm` reports an
Intel Mac.

Tarball contents: `hoot`, `LICENSE`, `README.md`. Stripping
the version from the filename keeps install scripts simple — version
lives in the URL path (`/releases/download/v0.0.1/...`) the same way
VSCode encodes commit in its update URL.

### Install paths

#### One-line curl-pipe (`install.sh`)

Hosted at `https://hootty.dev/install.sh` (or, until that's wired up,
`https://raw.githubusercontent.com/hayeah/hootty/master/install.sh`).
Pure POSIX shell, no bashisms. Behavior:

```sh
curl -fsSL https://hootty.dev/install.sh | sh
# or pin a version
curl -fsSL https://hootty.dev/install.sh | sh -s -- --version v0.0.1
```

What it does:

1. Detect OS/arch via `uname -sm`. Map to `{darwin,linux} × {amd64,arm64}`
   or fail with a clear "platform not supported" message.
2. Pick version: `--version` flag, else
   `https://api.github.com/repos/hayeah/hootty/releases/latest` (single
   curl, parse `tag_name` with `sed`/`grep` — no `jq` dep).
3. Download `hoot-<os>-<arch>.tar.gz` and `checksums.txt` to a tmp dir.
4. Verify the tarball SHA against the line in `checksums.txt`.
5. Extract, place `hoot` at `${HOOT_INSTALL_DIR:-$HOME/.local/bin}/hoot`
   (atomic via `mv`).
6. Print the install path and a "you may need to add this to PATH"
   reminder.

Failure modes to call out: glibc-too-old on Linux (run `hoot --help`
post-install; if it fails with a linker error, hint at glibc).

#### `hoot install-remote <host>` (local verb)

Mirrors Zed's two strategies with one extra:

1. Default: SSH to the host, run `command -v hoot` and `hoot version`,
   compare to local `Version`. If absent or mismatched, run the same
   `install.sh` *over the SSH connection* — i.e.
   `ssh <host> 'sh -s -- --version v0.0.1' < install.sh`. This works
   even on hosts with restricted egress only if the host can reach
   `objects.githubusercontent.com`. (Same as Zed's "fetch from server".)

2. `--upload`: same probe, but on miss, skip the remote download —
   download the tarball locally, `scp`/`sftp` it to the host, extract
   into place. Equivalent of Zed's `upload_binary_over_ssh: true`. Used
   when the remote can't reach GitHub.

3. `--from-file <tarball>`: dev escape hatch. Skip download entirely,
   ship a local tarball. Useful for testing a build before tagging.

`hoot install-remote` calls into `internal/bootstrap.Install(ctx, host,
opts)`. The same package is used by `hoot @host` (see below) so the
remote-bootstrap logic lives in one place.

#### `hoot @host` auto-bootstrap

Today, `internal/sshtransport/transport.go:remoteServeScript` runs
`hoot serve --bind ...` on the remote and assumes `hoot` is on PATH.
When it's not, the SSH tunnel never comes up and the user gets an
opaque "remote socket not ready" timeout.

Change: before launching the tunnel, run a one-shot probe over SSH:

```sh
hoot version 2>/dev/null || echo "MISSING"
```

- If output matches the local `Version`: proceed as today.
- If output is "MISSING" or a different version: invoke
  `internal/bootstrap.Install` (with the default
  fetch-from-github strategy) to install/upgrade, then re-probe. Cap
  retries at 1 (matching Zed's pattern).
- If the bootstrap fails (no curl on remote, no PATH dir writable, etc.),
  surface the error message inline and exit non-zero. Do **not** fall
  back to launching anyway.

This adds one extra round-trip on first connect. The probe is cheap (it
reuses the existing ssh control-master from
`sshtransport.ControlPath` so no new TCP handshake). After install, the
probe-pass path is identical to today's flow.

### Verification

Pre-tag dry runs:

- `pymake which release` — confirm the dep graph looks right (5 build
  rungs + the phony release).
- `pymake build:darwin-arm64 --vars hoot.version=v0.0.0-dev` — confirm
  `file dist/hoot-darwin-arm64/hoot` reports a Mach-O 64-bit arm64,
  `./dist/hoot-darwin-arm64/hoot version` prints `v0.0.0-dev`.
- `pymake build:linux-amd64 --vars hoot.version=v0.0.0-dev` — confirm
  `file` reports ELF x86-64, **statically linked**. Critically,
  `ldd dist/hoot-linux-amd64/hoot` reports "not a dynamic executable" —
  this is the musl-static invariant.
- `pymake build:linux-arm64 --vars hoot.version=v0.0.0-dev` — same
  shape, ELF aarch64, statically linked.
- `pymake -p -j 3 build --vars hoot.version=v0.0.0-dev` — all three
  in parallel; verifies no shared-state races (the libghostty per-target
  builds write to different prefixes so this should be clean).
- `pymake release --vars release.version=v0.0.0-rc1` — full dry run
  with a throwaway pre-release tag. Confirms preconditions + `gh release
  create` step. Delete the rc release after.

End-to-end smoke after tagging v0.0.1:

- `curl -fsSL https://raw.githubusercontent.com/hayeah/hootty/master/install.sh | sh`
  on a fresh Linux VM (e.g., `ssh devbox` after `rm -f ~/.local/bin/hoot`).
  Confirm `hoot version` prints `v0.0.1`.
- `hoot install-remote devbox` from macOS — confirm it places the
  matching binary on devbox.
- `hoot @devbox` from a clean state (no hoot on devbox) — confirm
  auto-bootstrap fires, installs, then attaches.

## Steps

(Becomes the worklog `## Todos`.)

### Phase 1 — release infrastructure

1. Add `cmd/hoot/version.go` with `var Version = "dev"` and a `version`
   subcommand that prints it. Wire into `main.go` dispatch.
2. Bootstrap `Makefile.py` with the `ghostty:fetch` and
   `libghostty:[T]` tasks. Local-test by running
   `pymake libghostty:darwin-arm64`, then `pymake libghostty:linux-amd64`,
   then `pymake libghostty:linux-arm64`. **This is where we verify the
   musl assumption** — if Ghostty's `build.zig` doesn't accept
   `-Dtarget=*-linux-musl`, surface here and fall back to `gnu.2.28`.
3. Add `hoot:[T]` and `tarball:[T]` tasks. Verify `ldd
   dist/hoot-linux-amd64/hoot` reports "not a dynamic executable".
4. Add `checksums` and the phony `release` task with preconditions.
   Wire `release.version` as a pymake var.
5. Tag `v0.0.0-rc1`, run `pymake release --vars release.version=v0.0.0-rc1`,
   fix the inevitable issues, delete the test release.
6. Tag `v0.0.1` for real once `rc1` is green.

### Phase 2 — install paths

7. Write `install.sh` (POSIX, no jq, no bashisms). Local-test by piping
   `cat install.sh | sh` with a stub release.
8. Add `internal/bootstrap` package — extract install logic into Go
   (probe-then-install, with the three modes: fetch-on-remote,
   upload-from-local, from-file).
9. Add `cmd/hoot/install_remote.go` calling into `internal/bootstrap`.
10. Modify `internal/sshtransport/transport.go` (or add a wrapping layer)
    to probe `hoot version` before tunneling and fall back to bootstrap.
    Keep the change small and well-tested — this is on the hot path of
    every `hoot @host`.
11. Smoke test all three flows on `devbox`.
12. Update `README.md` with the curl-pipe one-liner and a sentence about
    `hoot install-remote`. Add `docs/install.md` and `docs/releasing.md`.

## Open questions

All resolved during implementation:

- **Where does `install.sh` live for the curl-pipe URL?** Settled on
  `https://raw.githubusercontent.com/hayeah/hootty/master/install.sh` for
  v0.0.1. `hootty.dev` static site is a follow-up. Two-copy approach
  resolved separately (see Design notes 2026-05-09).
- **Does the `master` branch already have a `LICENSE` file?** No.
  `install.sh`'s tarball builder includes LICENSE only if present at the
  repo root; tarballs currently ship just `hoot` + `README.md`. Drops in
  automatically when LICENSE is added later.
- **Should `hoot install-remote` accept a version flag?** Yes —
  implemented as `--version vX.Y.Z`. Default pins to the local CLI's
  compiled-in `Version` (Zed-style: client and helper move in lockstep).
  Override is for the niche cases where the user knows what they want.
- **Does Ghostty's `build.zig` build cleanly for `*-linux-musl`?** Yes,
  verified end-to-end. `-Dtarget=x86_64-linux-musl` and
  `-Dtarget=aarch64-linux-musl` both produce statically-linked ELF
  binaries (`file` reports "statically linked"). No fallback to glibc
  needed.

## Design notes

- 2026-05-09T15:30Z — `install.sh` lives in two places, kept in sync by a test + a release precondition.
  - Force: `//go:embed` cannot reach above its package dir, but the curl-pipe URL needs the script at the repo root (`raw.githubusercontent.com/hayeah/hootty/master/install.sh`). And `hoot install-remote` / `hoot @host` need the same script embedded into the binary so they can pipe it over ssh — whatever's on master at curl-time isn't the right answer for an offline-built CLI shipped over scp.
  - Alternatives considered:
    - Single source at `internal/bootstrap/install.sh` only, document the curl URL deeper in the tree. Lost on URL ergonomics; users will not type `master/internal/bootstrap/install.sh`.
    - `go generate` directive that copies on demand. Lost on "easy to forget"; needs a CI guard anyway, so the test is the cheapest implementation.
    - Symlink at repo root → `internal/bootstrap/install.sh`. Lost because `raw.githubusercontent.com` serves the symlink target *path*, not the file contents; `curl | sh` would just print a path string.
  - Picked: literal byte-identical copy in both locations, enforced by `TestInstallShStaysInSync` + `_check_install_sh_in_sync` in `pymake release`. The release precondition catches drift even when no one ran the test pre-tag.

- 2026-05-09T15:30Z — Version threads through the build via `HOOT_VERSION` env var, not pymake task vars.
  - Force: pymake task vars are scoped per-task. The release flow needs the same version baked into `hoot:darwin-arm64`, `hoot:linux-amd64`, `hoot:linux-arm64`, AND a precondition check in `release` itself. Threading `--vars hoot:darwin-arm64.version=v0.0.1 --vars hoot:linux-amd64.version=v0.0.1 --vars hoot:linux-arm64.version=v0.0.1 --vars release.version=v0.0.1` is awful CLI ergonomics for the actual one-time-per-release act.
  - Picked: module-level `HOOT_VERSION = os.environ.get("HOOT_VERSION", "dev")` read at pymake-load time. The release task additionally re-invokes pymake with the same env var so the rebuild after `dist/` wipe inherits the right version. Tasks read `HOOT_VERSION` directly via Python closure, not via pymake's signature-introspection. Adds a slight repetition (the env var name appears in 4 places) but keeps the public ergonomic clean: `HOOT_VERSION=v0.0.1 pymake release`.
  - Trade-off: pymake's introspection won't show `version` in `pymake list --all`. Negligible — the release verb tells the user via its docstring and `docs/releasing.md`.

- 2026-05-09T15:30Z — `tree_digest.changed` does not compose with per-target `task.register`; switched to explicit Go-source enumeration.
  - Discovery: with three targets all gated on `run_if=HOOT_SRC_DIGEST.changed`, the *first* target's body would commit the digest, and the next two targets' `changed()` calls would return `False` — even when their outputs were missing. Caught when `pymake hoot:linux-amd64` reported `[skip] hoot:linux-amd64 (run_if returned False)` right after `pymake hoot:darwin-arm64` had finished, with no linux binary written.
  - The pymake docs warn about a related pitfall (output-dir-in-the-digest causing first-run flapping). The multi-consumer case isn't documented; should probably file a friction note upstream.
  - Workaround: enumerate `cmd/**/*.go + internal/**/*.go + go.mod + go.sum` (excluding `_test.go`) as explicit per-target inputs. Works correctly; doesn't catch new Go files added without a Makefile.py reload, but pymake re-imports `Makefile.py` on every run so that's handled too.

- 2026-05-08T18:00Z — Switched release-orchestration tooling from a flat shell script (`scripts/release.sh` + two helper scripts + Makefile target) to a pymake `Makefile.py`.
  - Trigger: maintainer asked "would it be suitable to use pymake?"
    during rfc. Yes — the build naturally factors into a dep graph
    (`ghostty:fetch → libghostty:[T] → hoot:[T] → tarball:[T] →
    checksums → release`) and pymake's file-mtime caching removes
    re-work during Phase 1's "rc1 → fix → rc2" iteration loop.
  - Specific wins over flat shell:
    - `tree_digest("cmd", "internal", "go.mod", "go.sum")` gates
      per-target Go rebuilds on real source change. With a flat script,
      every `release.sh` invocation rebuilds.
    - Per-target `task.register` loop is cleaner than three duplicated
      shell-script paths.
    - Free `pymake clean --all`, `pymake which release` (visualize
      plan), `pymake -p -j 3` (parallel), `pymake redo
      build:linux-amd64 --only` (retry one target).
  - Where pymake is awkward (called out in spec): the `gh release
    create` step is a "preconditions + side effect" thing, not "output
    file from input file". It maps to a phony task with `run_if`
    checks — workable, just not pymake's strongest pattern.
  - Alternative considered: a flat `scripts/release.sh` (the previous
    iteration). Lost on no caching during build iteration + three
    separate shell scripts to maintain.
  - Other alternative: GoReleaser. Already rejected in the previous
    iteration; not re-litigated here.
  - Onboarding cost: pymake is `uvx --from hayeah-pymake pymake …`,
    or `uv tool install` once. Maintainer wrote pymake, so familiarity
    is high.

- 2026-05-08T17:50Z — Switched Linux build from glibc-dynamic (`-target x86_64-linux-gnu.2.28`) to musl-static (`-target x86_64-linux-musl`).
  - Trigger: maintainer asked "isn't libghostty supposed to be glibc-free?"
    during rfc. They were right — `libghostty-vt.a` is a Zig static
    archive with empty `Libs.private` / `Requires.private`; it adds no
    libc dep on its own. The glibc dependency in the original spec was
    coming from cgo's Go runtime glue (malloc / pthread / errno /
    signals), not from libghostty-vt. So the choice we actually had was
    *which* libc, not whether to need one — and musl-static is strictly
    better for hoot's "ssh into arbitrary Linux boxes" use case.
  - Alternatives considered:
    - **glibc-dynamic, floor 2.28** (original): works on most modern
      Linux, but introduces a glibc-floor decision, won't run on
      Alpine without `gcompat`, won't run in distroless. Lost on
      "another decision to make + niche failure cases" vs. musl's
      "just runs everywhere".
    - **glibc-static**: not a real option. glibc is officially
      not statically linkable (NSS / threading / locale).
    - **musl-static** (picked): `zig cc -target *-linux-musl` produces
      fully static ELFs. Runs on Ubuntu, Alpine, distroless, RHEL 7,
      embedded boxes. ~+500KB–1MB per binary for bundled musl. No
      glibc floor to document.
  - Caveats noted in spec:
    - Unverified that Ghostty's `build.zig` (`-Demit-lib-vt=true
      -Dapp-runtime=none`) compiles cleanly for `*-linux-musl`. Should,
      because Zig handles musl natively and the VT target is small,
      but Phase 1 step 2 verifies; if it breaks on a glibc-ism in
      Ghostty's build tree, fall back to `gnu.2.28`.
    - Go cgo + musl historic gotchas (DNS via nsswitch vs `/etc/resolv.conf`,
      locale tables): hoot doesn't do DNS from inside the binary
      (openssh handles ssh resolution, internal connections are unix
      sockets / known IPs) and isn't locale-sensitive, so the typical
      breakage paths don't apply.
  - Pattern matches: ripgrep / fd / bat / delta all ship musl-static
    on Linux. Go tools usually don't bother because they default to
    `CGO_ENABLED=0`; we don't have that escape hatch (cgo is forced
    by libghostty-vt).
  - Follow-on: removed the "glibc floor" open question (moot under
    musl-static); added an "ldd reports 'not a dynamic executable'"
    invariant check to the Verification section.

- 2026-05-08T17:35Z — Switched release pipeline from GitHub Actions to a local shell script driven from the maintainer's macOS host.
  - First draft of the spec proposed a 4-job GH Actions matrix
    (later 3 after dropping darwin-amd64). Maintainer pushed back during
    rfc: "not sure I want to bother with GitHub Actions" — fair, given
    the build's hard parts (Zig 0.15.2, pinned Ghostty source, libghostty-vt
    CMake, pkg-config) all already work on their laptop. CI would be
    re-installing that toolchain every release for no qualitative gain.
  - Alternatives considered:
    - **GH Actions** (original plan): on-tag automation, audit trail of
      what built each release, reproducibility-by-multiple-builders.
      Lost on: hours-of-CI-debugging cost to make Zig/CMake/Ghostty
      source provisioning work on hosted runners; single-developer +
      private repo means none of the wins are load-bearing yet.
    - **GoReleaser**: even less of a fit — its `builds:` block expects
      Go-only or one CC override, and we need a per-target shell step
      to build libghostty-vt before `go build`. Already rejected in
      previous iteration; recording here so a future reader doesn't
      re-litigate.
    - **Local shell script + `gh release create`** (picked): macOS host
      builds darwin-arm64 native and linux-{amd64,arm64} via `zig cc`
      cross. Zero CI infrastructure. The `docs/cross-compile.md` recipe
      already validates the linux cross; arm64 is one extra `-target`
      line.
  - Trade-offs accepted: no audit trail / no SLSA, no on-tag CI sanity,
    single-laptop point of failure. Documented in spec under "Tool choice"
    so a future contributor knows what they'd be giving up by *not*
    adopting CI when they need it.
  - Bonus from local: glibc floor becomes deterministic
    (`-target x86_64-linux-gnu.2.28`) instead of inheriting whatever the
    CI runner's Ubuntu version ships.

- 2026-05-08T17:25Z — Dropped `darwin-amd64` from the v0.0.1 build matrix.
  - Originally proposed both darwin arches per the section text's
    "both common architectures". Maintainer clarified during rfc:
    arm64 only for v0.0.1 — no Intel Macs in their stable. Section
    text framing was generic, not a hard requirement.
  - Effect: `install.sh` must explicitly reject darwin/x86_64 with a
    helpful message, not silently fall back. Added a note next to the
    archive list. If an Intel-Mac user surfaces later, re-adding the
    `macos-13` runner + one matrix line is a small re-enable, not a
    redesign.
  - Trade-off considered: keeping darwin-amd64 in case the build
    breaks for Apple Silicon-only reasons (e.g., a future cgo dep
    that needs Rosetta to test on x86) — rejected as speculative.
