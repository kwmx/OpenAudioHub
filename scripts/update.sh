#!/bin/bash
# Install a published OpenAudioHub release.
#
# The check itself lives in the daemon (Go, unit-tested). This script only
# performs the install, so it can also be run by hand over SSH:
#
#   sudo /usr/local/lib/openaudiohub/update.sh install v0.2.0
#
# It downloads the release source archive for the tag, refuses anything that does
# not look like this project, and hands off to the normal installer.
set -euo pipefail

REPO="${OPENAUDIOHUB_REPO:-kwmx/OpenAudioHub}"
MODE="${1:-check}"
TAG="${2:-}"

say() { echo "$*"; }
die() { echo "$*" >&2; exit 1; }

current_version() {
  /usr/local/bin/openaudiohubd --version 2>/dev/null | tr -d ' \n' || echo unknown
}

latest_tag() {
  curl -fsSL --max-time 15 -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

case "$MODE" in
  check)
    say "installed: $(current_version)"
    t="$(latest_tag || true)"
    [[ -n "$t" ]] || { say "No published releases found."; exit 0; }
    say "latest:    $t"
    ;;
  install)
    [[ -n "$TAG" ]] || die "install needs a release tag"
    [[ ${EUID:-$(id -u)} -eq 0 ]] || exec sudo -E bash "$0" "$@"

    say "Installing OpenAudioHub $TAG (currently $(current_version))"
    WORK="$(mktemp -d /var/tmp/openaudiohub-update.XXXXXX)"
    trap 'rm -rf -- "$WORK"' EXIT

    URL="https://github.com/${REPO}/archive/refs/tags/${TAG}.tar.gz"
    say "Downloading $URL"
    curl -fsSL --max-time 300 -o "$WORK/src.tar.gz" "$URL" \
      || die "Download failed. Check the network and the tag name."

    mkdir -p "$WORK/src"
    tar -xzf "$WORK/src.tar.gz" -C "$WORK/src" --strip-components=1 \
      || die "The release archive could not be extracted."

    # Refuse anything that is not this project, before running code from it.
    [[ -x "$WORK/src/scripts/install.sh" ]] \
      || die "The archive does not contain scripts/install.sh; refusing to continue."
    [[ -f "$WORK/src/Makefile" ]] \
      || die "The archive does not look like OpenAudioHub; refusing to continue."
    grep -q 'openaudiohub' "$WORK/src/Makefile" \
      || die "The archive does not look like OpenAudioHub; refusing to continue."

    say "Running the installer from the release archive"
    bash "$WORK/src/scripts/install.sh"

    say "Done. Installed version: $(current_version)"
    ;;
  *)
    die "usage: update.sh [check | install <tag>]"
    ;;
esac
