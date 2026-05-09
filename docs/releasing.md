# Releasing hoot

Releases are cut from a single macOS host (the maintainer's laptop) using
[`pymake`](https://github.com/hayeah/pymake) against `Makefile.py`. No
GitHub Actions; the on-tag step is `pymake release`. See `spec.md` design
notes for why.

## Prereqs

- Go 1.26
- Zig 0.15.2 (`mise use -g zig@0.15.2` — Zig 0.16 does NOT work)
- CMake, pkg-config
- `gh` CLI authenticated to the repo (`gh auth login`)
- pymake (`uv tool install hayeah-pymake` or `uvx --from hayeah-pymake pymake`)

## Cutting a release

Pre-flight: tests pass, README reflects new behavior, docs in `docs/` updated.

```sh
# 1. Decide on the version. Semver, prefix v.
VERSION=v0.0.2

# 2. Tag at HEAD and push. (Lightweight tag is fine.)
git tag $VERSION
git push origin $VERSION

# 3. Cut the release. HOOT_VERSION threads the tag through hoot:[T] -ldflags
#    so the binaries embed the matching version string.
HOOT_VERSION=$VERSION pymake release
```

`pymake release` is gated on these preconditions (fail-fast, in order):

- `HOOT_VERSION` is set and not "dev"
- working tree is clean (no uncommitted/staged changes)
- HEAD matches the tag named by `HOOT_VERSION` (`git describe --tags --exact-match`)
- no GitHub release for that tag already exists
- `gh` CLI is authenticated
- the curl-pipe `install.sh` and the embedded `internal/bootstrap/install.sh`
  are byte-identical (a test enforces this too, but the precondition catches
  divergence even when no one ran the test)

When all pass, the task wipes `dist/`, re-invokes `pymake tarballs checksums`
under the same `HOOT_VERSION` env (so the binaries get the right tag baked
in), then `gh release create` with the four artifacts:

- `hoot-darwin-arm64.tar.gz`
- `hoot-linux-amd64.tar.gz`
- `hoot-linux-arm64.tar.gz`
- `checksums.txt` (SHA-256 in `shasum -a 256` format)

`v0.0.x` tags ship as **prereleases** — they're hidden from
`https://api.github.com/repos/hayeah/hootty/releases/latest`, which means
the curl-pipe `install.sh` won't auto-pick them up. Users who want a
prerelease have to pass `--version vX.Y.Z` explicitly. This is intentional
("no stability guarantee yet"). Drop the prerelease flag in `Makefile.py`
once we tag a non-`0.0.x` release.

## Dry-running the release flow

To exercise the publish path without burning a real tag, push a
`v0.0.0-rc<N>` tag, run `pymake release`, verify, then delete:

```sh
git tag v0.0.0-rc1
git push origin v0.0.0-rc1
HOOT_VERSION=v0.0.0-rc1 pymake release

# verify (sample)
gh release view v0.0.0-rc1 --json assets --jq '.assets[] | "\(.name) \(.size)"'

# clean up
gh release delete v0.0.0-rc1 --cleanup-tag --yes
git tag -d v0.0.0-rc1
```

## Recovering from a failed publish

The pipeline up through `checksums` is idempotent — re-running rebuilds
only what's stale. The publish step (`gh release create`) is the
irreversible one. Common failure: `gh` auth expired mid-run.

Recover:

```sh
gh auth status                  # confirm reauth
HOOT_VERSION=$VERSION pymake release   # re-runs; preconditions still hold
```

If `gh release create` *partially* succeeded — release exists but missing
some assets — the `_check_release_does_not_exist` precondition will refuse
the next run. Either delete the partial release and re-run, or `gh release
upload` the missing assets manually.

## Tasks reference

```
pymake which release           # visualize the dep graph
pymake build                   # build all 3 targets at HOOT_VERSION (default "dev")
pymake build:darwin-arm64      # one target
pymake -p -j 3 build           # parallel
pymake clean --all             # wipe dist/ + per-target build artifacts
pymake tarballs                # build, stage, tar.gz all 3 targets
pymake checksums               # recompute dist/checksums.txt
pymake release                 # full publish (requires HOOT_VERSION, etc.)
```

`.build/ghostty/src` (cloned at the pinned Ghostty SHA) and
`.build/libghostty/<target>/` (per-target zig artifacts) are gitignored
and reused across runs. `pymake clean --all` removes both `dist/` and the
build-tree artifacts.

## Bumping the Ghostty pin

The pinned Ghostty SHA is read from
`mitchellh/go-libghostty/CMakeLists.txt`. To bump:

- Bump `go-libghostty` itself (`go get github.com/mitchellh/go-libghostty@<new>`)
  in `go.mod`.
- Update `GHOSTTY_SHA` in `Makefile.py` to match the new
  `CMakeLists.txt`'s `GIT_TAG`.
- Run `pymake -B build` to verify all three targets still build.
