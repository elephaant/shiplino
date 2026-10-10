#!/bin/sh
# Copyright 2026 The Shiplino Authors
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# Shiplino installer (macOS, Linux).
#
#   curl -fsSL https://raw.githubusercontent.com/elephaant/shiplino/main/scripts/install.sh | sh
#
# Downloads the release for this OS/CPU, verifies its SHA-256 checksum
# (and the Sigstore signature of the checksum file when `cosign` is
# installed), installs to ~/.shiplino/bin and runs `shiplino setup`.
#
# Environment:
#   SHIPLINO_VERSION         release tag to install (default: newest)
#   SHIPLINO_HOME            install location (default: ~/.shiplino)
#   SHIPLINO_NO_SETUP=1      install the binary only
#   SHIPLINO_SETUP_ARGS      extra arguments for `shiplino setup`
#   SHIPLINO_DOWNLOAD_BASE   override the download URL (testing, mirrors)
set -eu

REPO="elephaant/shiplino"

say() { printf '%s\n' "$*"; }
fail() {
  printf 'shiplino install: %s\n' "$*" >&2
  exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || fail "'$1' is required"; }

download() { # url dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    fail "curl or wget is required"
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    fail "sha256sum or shasum is required to verify the download"
  fi
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m)" ;;
esac
need tar
need uname

version="${SHIPLINO_VERSION:-}"
if [ -z "$version" ]; then
  tmpjson=$(mktemp)
  download "https://api.github.com/repos/$REPO/releases?per_page=1" "$tmpjson" || fail "can't reach GitHub to find the latest release"
  version=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmpjson" | head -n 1)
  rm -f "$tmpjson"
  [ -n "$version" ] || fail "no release found"
fi
num=${version#v}
base="${SHIPLINO_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download/$version}"
archive="shiplino_${num}_${os}_${arch}.tar.gz"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

say "Downloading Shiplino $version for $os/$arch…"
download "$base/$archive" "$work/$archive" || fail "download failed: $base/$archive"
download "$base/checksums.txt" "$work/checksums.txt" || fail "download failed: $base/checksums.txt"

if command -v cosign >/dev/null 2>&1; then
  if download "$base/checksums.txt.sigstore.json" "$work/checksums.txt.sigstore.json" 2>/dev/null; then
    cosign verify-blob \
      --bundle "$work/checksums.txt.sigstore.json" \
      --certificate-identity-regexp "^https://github.com/$REPO/\.github/workflows/release\.yml@refs/tags/v" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      "$work/checksums.txt" >/dev/null 2>&1 || fail "signature check FAILED for checksums.txt; not installing"
    say "Signature verified (Sigstore)."
  else
    fail "signature bundle missing for $version; not installing"
  fi
else
  say "Note: install 'cosign' to also verify the release signature."
fi

want=$(grep " ${archive}\$" "$work/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || fail "$archive is not listed in checksums.txt"
got=$(sha256 "$work/$archive")
[ "$want" = "$got" ] || fail "checksum mismatch for $archive (expected $want, got $got); not installing"
say "Checksum verified."

tar -xzf "$work/$archive" -C "$work" shiplino || fail "archive doesn't contain the shiplino binary"

home="${SHIPLINO_HOME:-$HOME/.shiplino}"
mkdir -p "$home/bin"
chmod 700 "$home"
# Replace atomically so running hooks never see a half-written binary.
cp "$work/shiplino" "$home/bin/.shiplino.new"
chmod 755 "$home/bin/.shiplino.new"
mv -f "$home/bin/.shiplino.new" "$home/bin/shiplino"
say "Installed $("$home/bin/shiplino" version) to $home/bin/shiplino"

# Put it on PATH via ~/.local/bin when that directory is already on PATH.
case ":$PATH:" in
*":$HOME/.local/bin:"*)
  mkdir -p "$HOME/.local/bin"
  ln -sf "$home/bin/shiplino" "$HOME/.local/bin/shiplino"
  ;;
*) say "Tip: add $home/bin to your PATH to run 'shiplino' directly." ;;
esac

if [ "${SHIPLINO_NO_SETUP:-}" = "1" ]; then
  say "Skipping setup (SHIPLINO_NO_SETUP=1). Run: $home/bin/shiplino setup"
  exit 0
fi
say ""
# shellcheck disable=SC2086 # SHIPLINO_SETUP_ARGS is intentionally split
SHIPLINO_HOME="$home" exec "$home/bin/shiplino" setup ${SHIPLINO_SETUP_ARGS:-}
