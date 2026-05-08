"""Release pipeline for hoot.

Build graph (per target T in TARGETS):

  ghostty:fetch         clone Ghostty at the pinned SHA into .build/ghostty/src
        ↓
  libghostty:[T]        zig build of libghostty-vt for the target triple
        ↓
  hoot:[T]              go build of cmd/hoot linked against libghostty:[T]
        ↓
  tarball:[T]           tar.gz of dist/hoot-<os>-<arch>/{hoot,LICENSE,README.md}
        ↓
  checksums             SHA-256 of all tarballs
        ↓
  release (phony)       gh release create — gated on clean tree, tag, gh auth

Usage:

  pymake which release           # visualize plan
  pymake build                   # build all targets at hoot.version=dev
  pymake build:darwin-arm64      # one target
  pymake -p -j 3 build           # parallel
  pymake release --vars release.version=v0.0.1
"""

import os
import shlex
import subprocess
from pathlib import Path

from pymake import sh, task

# ---- pinned versions (bump when go-libghostty bumps Ghostty) -----------------

# Ghostty SHA pinned in mitchellh/go-libghostty/CMakeLists.txt. Read by hand on
# bump; we don't auto-derive from go.mod because that's a separate concern.
GHOSTTY_SHA = "9e080c5a403475dcbee93c40eeb22cf6f92121f4"
GHOSTTY_REPO = "https://github.com/ghostty-org/ghostty.git"

# ---- targets -----------------------------------------------------------------

# Each target maps {os, arch} -> zig target triple. darwin-arm64 is built
# natively (Ghostty's libghostty-vt static archive doesn't need a special
# triple; the host clang links it in). Linux uses musl for fully-static output.
TARGETS = {
    "darwin-arm64": {
        "os": "darwin",
        "arch": "arm64",
        "zig_triple": "aarch64-macos-none",
        "linux": False,
    },
    "linux-amd64": {
        "os": "linux",
        "arch": "amd64",
        "zig_triple": "x86_64-linux-musl",
        "linux": True,
    },
    "linux-arm64": {
        "os": "linux",
        "arch": "arm64",
        "zig_triple": "aarch64-linux-musl",
        "linux": True,
    },
}

# ---- paths -------------------------------------------------------------------

REPO = Path(__file__).parent.resolve()
BUILD = REPO / ".build"
DIST = REPO / "dist"

GHOSTTY_SRC = BUILD / "ghostty" / "src"
GHOSTTY_STAMP = BUILD / "ghostty" / f".fetched-{GHOSTTY_SHA}"


def libghostty_prefix(t: str) -> Path:
    return BUILD / "libghostty" / t


def libghostty_archive(t: str) -> Path:
    return libghostty_prefix(t) / "lib" / "libghostty-vt.a"


def libghostty_pkgconfig(t: str) -> Path:
    return libghostty_prefix(t) / "share" / "pkgconfig"


def hoot_binary(t: str) -> Path:
    return DIST / f"hoot-{t}" / "hoot"


def hoot_tarball(t: str) -> Path:
    return DIST / f"hoot-{t}.tar.gz"


CHECKSUMS = DIST / "checksums.txt"


# ---- Version -----------------------------------------------------------------
#
# HOOT_VERSION env var threads the release tag through hoot:[T] -> tarball:[T] ->
# checksums -> release. "dev" is the unreleased default. The release task refuses
# to publish unless HOOT_VERSION is set to a real semver tag and matches HEAD.
HOOT_VERSION = os.environ.get("HOOT_VERSION", "dev")


# ---- Go source enumeration --------------------------------------------------
#
# Per-target Go builds re-run when any non-test .go file under cmd/ + internal/
# (or repo-root .go files, or go.mod / go.sum) is newer than the binary.
# `tree_digest.changed` would be cleaner, but its post-body auto-commit means
# only one of N consumers ever sees "changed=True" per pymake invocation; with
# three target builds sharing one digest, two of them silently skip even when
# their output is missing. Explicit input enumeration sidesteps the problem.
def _go_source_inputs():
    paths: list[Path] = []
    for pattern in ("*.go", "cmd/**/*.go", "internal/**/*.go"):
        paths.extend(REPO.glob(pattern))
    paths = [p for p in paths if not p.name.endswith("_test.go")]
    paths.append(REPO / "go.mod")
    paths.append(REPO / "go.sum")
    return sorted(set(paths))


HOOT_SOURCES = _go_source_inputs()


# ---- ghostty:fetch -----------------------------------------------------------


@task(outputs=[GHOSTTY_STAMP])
def ghostty_fetch():
    """Clone Ghostty into .build/ghostty/src and check out the pinned SHA."""
    GHOSTTY_SRC.parent.mkdir(parents=True, exist_ok=True)
    if not GHOSTTY_SRC.exists():
        sh(f"git clone --filter=blob:none {GHOSTTY_REPO} {GHOSTTY_SRC}")
    # Cheap if the SHA is already in the local pack; otherwise fetches it.
    sh(f"git -C {GHOSTTY_SRC} fetch --filter=blob:none origin {GHOSTTY_SHA}")
    sh(f"git -C {GHOSTTY_SRC} -c advice.detachedHead=false checkout --force {GHOSTTY_SHA}")
    GHOSTTY_STAMP.write_text(GHOSTTY_SHA + "\n")


# ---- libghostty:[T] (per target) --------------------------------------------


def _register_libghostty():
    for t, meta in TARGETS.items():
        triple = meta["zig_triple"]
        archive = libghostty_archive(t)
        prefix = libghostty_prefix(t)

        def build(triple=triple, prefix=prefix):
            prefix.mkdir(parents=True, exist_ok=True)
            sh(
                f"cd {GHOSTTY_SRC} && zig build "
                f"-Dtarget={triple} "
                f"-Dcpu=baseline "
                f"-Doptimize=ReleaseFast "
                f"-Dapp-runtime=none "
                f"-Demit-lib-vt=true "
                f"--prefix {prefix}"
            )

        task.register(
            build,
            name=f"libghostty:{t}",
            inputs=[GHOSTTY_STAMP],
            outputs=[archive],
        )


_register_libghostty()


# ---- meta task: build all libghostty targets --------------------------------


@task(
    inputs=[libghostty_archive(t) for t in TARGETS],
)
def libghostty():
    """Build libghostty-vt for every target."""
    pass


# ---- hoot:[T] (per target) --------------------------------------------------


def _register_hoot_builds():
    for t, meta in TARGETS.items():
        os_, arch, triple = meta["os"], meta["arch"], meta["zig_triple"]
        is_linux = meta["linux"]
        binary = hoot_binary(t)
        archive = libghostty_archive(t)
        pkgconfig = libghostty_pkgconfig(t)

        def build(
            os_=os_,
            arch=arch,
            triple=triple,
            is_linux=is_linux,
            binary=binary,
            pkgconfig=pkgconfig,
        ):
            binary.parent.mkdir(parents=True, exist_ok=True)
            ldflags = f"-s -w -X main.Version={HOOT_VERSION}"
            cc_prefix = ""
            if is_linux:
                ldflags += " -extldflags=-static"
                cc_prefix = f"CC='zig cc -target {triple}' "
            sh(
                f"PKG_CONFIG_PATH={pkgconfig} "
                f"CGO_ENABLED=1 GOOS={os_} GOARCH={arch} GOWORK=off "
                f"{cc_prefix}"
                f"go -C {REPO} build -trimpath "
                f"-ldflags \"{ldflags}\" "
                f"-o {binary} ./cmd/hoot"
            )

        task.register(
            build,
            name=f"hoot:{t}",
            inputs=[archive, *HOOT_SOURCES],
            outputs=[binary],
        )


_register_hoot_builds()


# ---- tarball:[T] (per target) -----------------------------------------------


def _register_tarballs():
    license_path = REPO / "LICENSE"
    readme_path = REPO / "README.md"
    extras = [readme_path]
    if license_path.exists():
        extras.append(license_path)

    for t in TARGETS:
        binary = hoot_binary(t)
        tarball = hoot_tarball(t)
        stage = binary.parent

        def build(t=t, binary=binary, tarball=tarball, stage=stage, extras=extras):
            for extra in extras:
                sh(f"cp {extra} {stage}/")
            sh(f"tar -czf {tarball} -C {stage} .")

        task.register(
            build,
            name=f"tarball:{t}",
            inputs=[binary, *extras],
            outputs=[tarball],
        )


_register_tarballs()


# ---- top-level meta tasks ---------------------------------------------------


@task(inputs=[hoot_binary(t) for t in TARGETS])
def build():
    """Build hoot for every target (no tarballing)."""
    pass


@task(inputs=[hoot_tarball(t) for t in TARGETS])
def tarballs():
    """Build all tarballs."""
    pass


# ---- checksums --------------------------------------------------------------


@task(inputs=[hoot_tarball(t) for t in TARGETS], outputs=[CHECKSUMS])
def checksums():
    """SHA-256 of all tarballs into dist/checksums.txt."""
    sh(f"cd {DIST} && shasum -a 256 hoot-*.tar.gz > checksums.txt")


# ---- release (phony) --------------------------------------------------------


def _check_clean_tree():
    """Refuse to release if the worktree has uncommitted or staged changes."""
    if subprocess.run(["git", "diff", "--quiet"], cwd=REPO).returncode != 0:
        raise SystemExit("release: working tree has uncommitted changes")
    if subprocess.run(["git", "diff", "--cached", "--quiet"], cwd=REPO).returncode != 0:
        raise SystemExit("release: staged changes are not committed")


def _check_tag_matches_head(version: str):
    """HEAD must be at exactly the tag we're releasing."""
    res = subprocess.run(
        ["git", "describe", "--exact-match", "HEAD"],
        cwd=REPO,
        capture_output=True,
        text=True,
    )
    if res.returncode != 0:
        raise SystemExit(
            f"release: HEAD is not on a tag. Run `git tag {version}` first."
        )
    found = res.stdout.strip()
    if found != version:
        raise SystemExit(
            f"release: HEAD tag is {found!r}, expected {version!r}. Aborting."
        )


def _check_release_does_not_exist(version: str):
    """gh release view returns 0 if the release exists; we want it to NOT exist."""
    res = subprocess.run(
        ["gh", "release", "view", version],
        cwd=REPO,
        capture_output=True,
    )
    if res.returncode == 0:
        raise SystemExit(
            f"release: GitHub release {version} already exists. "
            "Delete it manually if you really want to recreate."
        )


def _check_gh_auth():
    res = subprocess.run(["gh", "auth", "status"], capture_output=True)
    if res.returncode != 0:
        raise SystemExit("release: gh CLI is not authenticated. Run `gh auth login`.")


@task()
def release():
    """Cut a GitHub release for $HOOT_VERSION (must be set, must match HEAD tag)."""
    if HOOT_VERSION == "dev":
        raise SystemExit(
            "release: HOOT_VERSION not set. Invoke as: "
            "HOOT_VERSION=v0.0.1 pymake release"
        )

    _check_clean_tree()
    _check_tag_matches_head(HOOT_VERSION)
    _check_release_does_not_exist(HOOT_VERSION)
    _check_gh_auth()

    # The hoot binary embeds Version via -ldflags. If dist/ already has binaries
    # built under a different HOOT_VERSION (e.g. "dev"), we'd ship a binary whose
    # `hoot version` disagrees with the tag. Wipe and rebuild to be safe.
    if DIST.exists():
        sh(f"rm -rf {DIST}")

    # Re-invoke pymake to rebuild from scratch with the current HOOT_VERSION env.
    # `tarballs` triggers hoot:[T] -> tarball:[T] -> tarballs. Then `checksums`.
    sh(f"HOOT_VERSION={shlex.quote(HOOT_VERSION)} pymake tarballs checksums")

    notes = (
        f"hoot {HOOT_VERSION}\n\n"
        "See https://github.com/hayeah/hootty/blob/master/README.md for usage."
    )
    is_prerelease = "0.0." in HOOT_VERSION  # v0.0.x is pre-stability per spec
    flags = "--prerelease " if is_prerelease else ""
    sh(
        f"cd {DIST} && gh release create {HOOT_VERSION} "
        f"--title {HOOT_VERSION} {flags}"
        f"--notes {shlex.quote(notes)} "
        f"hoot-darwin-arm64.tar.gz hoot-linux-amd64.tar.gz hoot-linux-arm64.tar.gz "
        f"checksums.txt"
    )


task.default(build)
