#!/bin/sh
# Kiln installer for Linux, macOS and FreeBSD.
#
#   curl -fsSL https://github.com/pixelpolishers/kiln-cli/releases/latest/download/install.sh | sh
#   wget -qO- https://github.com/pixelpolishers/kiln-cli/releases/latest/download/install.sh | sh
#
# It finds out which system and processor this is, downloads the matching kiln
# archive from the GitHub release, checks its SHA-256 checksum, and installs the
# `kiln` program. Nothing else on the machine is changed.
#
# Settings (environment variables):
#   KILN_VERSION      the version to install, e.g. v0.1.0 (default: the newest stable
#                     release, or the newest release candidate while there is no stable one)
#   KILN_INSTALL_DIR  where to put `kiln` (default: /usr/local/bin when run as root,
#                     otherwise ~/.local/bin)
#
# The copy of this script that is attached to a release has the checksums of that
# release baked in (between the BAKED markers below); the copy in the repository has
# none and takes them from the release's SHA256SUMS file instead.

# Everything happens inside main, called on the last line, so a download that is
# cut off halfway does not run a half-read script.
main() {
  set -eu

  repo="pixelpolishers/kiln-cli"

  # BAKED-BEGIN (filled in by the release workflow)
  baked_version=""
  baked_sums=""
  # BAKED-END

  say() { printf '%s\n' "$*"; }
  fail() { printf 'kiln install: %s\n' "$*" >&2; exit 1; }
  have() { command -v "$1" >/dev/null 2>&1; }

  # download URL FILE
  download() {
    if have curl; then
      curl -fsSL "$1" -o "$2" || return 1
    elif have wget; then
      wget -q "$1" -O "$2" || return 1
    else
      fail "neither curl nor wget is installed"
    fi
  }

  # print URL (to standard output)
  fetch() {
    if have curl; then
      curl -fsSL "$1"
    elif have wget; then
      wget -qO- "$1"
    else
      fail "neither curl nor wget is installed"
    fi
  }

  sha256_of() {
    if have sha256sum; then
      sha256sum "$1" | cut -d ' ' -f 1
    elif have shasum; then
      shasum -a 256 "$1" | cut -d ' ' -f 1
    elif have openssl; then
      openssl dgst -sha256 "$1" | sed 's/^.*= *//'
    else
      fail "cannot check the download: none of sha256sum, shasum or openssl is installed"
    fi
  }

  # ---- which system is this? ----
  case "$(uname -s)" in
    Linux)   os=linux ;;
    Darwin)  os=darwin ;;
    FreeBSD) os=freebsd ;;
    MINGW*|MSYS*|CYGWIN*) fail "this is Windows: use install.ps1 in PowerShell instead (see https://github.com/$repo#windows)" ;;
    *) fail "unsupported operating system: $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "unsupported processor: $(uname -m)" ;;
  esac
  asset="kiln_${os}_${arch}.tar.gz"
  have tar || fail "tar is required"

  # ---- which version? ----
  version="${KILN_VERSION:-$baked_version}"
  if [ -z "$version" ]; then
    # The newest stable release, from the releases feed (no API, so no rate limit for
    # people who share an address). The "latest" release of GitHub ignores
    # pre-releases, so while there is no stable release fall back to the newest of any kind.
    tags="$(fetch "https://github.com/$repo/releases.atom" 2>/dev/null | sed -n 's|.*/releases/tag/\([^"<]*\)".*|\1|p' || true)"
    [ -n "$tags" ] || fail "cannot find a release of $repo (no network, or none published yet)"
    version="$(printf '%s\n' "$tags" | grep -v -e '-' | head -n 1 || true)"
    if [ -z "$version" ]; then
      version="$(printf '%s\n' "$tags" | head -n 1)"
      say "There is no stable release yet; installing the newest pre-release, $version."
    fi
  fi
  case "$version" in v*) ;; *) version="v$version" ;; esac
  base="https://github.com/$repo/releases/download/$version"

  # ---- download ----
  tmp="$(mktemp -d 2>/dev/null || mktemp -d -t kiln)"
  trap 'rm -rf "$tmp"' EXIT INT TERM HUP
  say "Downloading Kiln $version for $os/$arch ..."
  download "$base/$asset" "$tmp/$asset" || fail "cannot download $base/$asset (is there a $version release with a $os/$arch binary?)"

  # ---- check the checksum ----
  expected=""
  if [ -n "$baked_sums" ] && [ "$version" = "$baked_version" ]; then
    expected="$(printf '%s\n' "$baked_sums" | awk -v f="$asset" '$2 == f { print $1 }')"
    origin="this script"
  fi
  if [ -z "$expected" ]; then
    download "$base/SHA256SUMS" "$tmp/SHA256SUMS" || fail "cannot download $base/SHA256SUMS"
    expected="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/SHA256SUMS")"
    origin="the release's SHA256SUMS"
  fi
  [ -n "$expected" ] || fail "no checksum for $asset"
  actual="$(sha256_of "$tmp/$asset")"
  if [ "$actual" != "$expected" ]; then
    fail "checksum mismatch for $asset: expected $expected (from $origin), got $actual. Nothing was installed."
  fi
  say "Checksum OK (from $origin)."

  # ---- install ----
  tar -xzf "$tmp/$asset" -C "$tmp" kiln || fail "cannot unpack $asset"
  if [ -n "${KILN_INSTALL_DIR:-}" ]; then
    dir="$KILN_INSTALL_DIR"
  elif [ "$(id -u)" = 0 ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
  fi
  mkdir -p "$dir" || fail "cannot create $dir"
  # copy under another name, then rename: the replacement is atomic and works while kiln runs
  cp "$tmp/kiln" "$dir/.kiln.new" || fail "cannot write to $dir (set KILN_INSTALL_DIR, or run as root)"
  chmod 755 "$dir/.kiln.new"
  mv -f "$dir/.kiln.new" "$dir/kiln"
  say "Installed $("$dir/kiln" version) in $dir/kiln"

  case ":$PATH:" in
    *":$dir:"*) ;;
    *) say ""
       say "$dir is not on your PATH. Add it, for example:"
       say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile    # then open a new terminal" ;;
  esac
  say ""
  say "Next: run 'kiln settings' to see what Kiln found on this machine, and"
  say "'kiln install --check' in a project to see which tools are missing."
}

main "$@"
