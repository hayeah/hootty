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

from pathlib import Path

from pymake import sh, task, tree_digest

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

# Source-tree digest for hoot itself. Gates per-target Go rebuilds on real
# source change. Excludes the build/dist trees and the worktree's go.work
# (which is gitignored anyway).
HOOT_SRC_DIGEST = tree_digest(
    REPO / "cmd",
    REPO / "internal",
    REPO,  # picks up go.mod / go.sum / *.go at the root
    digest=BUILD / "hoot-src.digest",
    exclude=[".build/", "dist/", ".worktrees/", "node_modules/"],
    globs=["**/*.go", "**/go.mod", "**/go.sum"],
)


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


task.default(libghostty)
