#!/bin/bash
# Builds an isolated daemon. Never make install and never replace /usr/bin/bluealsa.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PIN=f11569451b98a765adf9abc081632bb013f89f57
DEST="${OPENAUDIOHUB_BLUEALSA_DEST:-/usr/local/lib/openaudiohub/bluealsa-4.3.1-oah}"
SOURCE="${OPENAUDIOHUB_BLUEALSA_SOURCE:-}"
if [[ ${EUID:-$(id -u)} -ne 0 ]]; then exec sudo -E bash "$0" "$@"; fi
case "${1:-}" in --help|-h)
  echo "sudo OPENAUDIOHUB_BLUEALSA_SOURCE=/home/USER/src/bluez-alsa-bp35 bash scripts/build-bluealsa.sh"
  echo "Existing Git objects at the pinned commit are exported cleanly. The worktree is never modified."
  exit 0;; esac
export DEBIAN_FRONTEND=noninteractive
apt-get install -y --no-install-recommends git build-essential autoconf automake libtool pkg-config \
  libasound2-dev libbluetooth-dev libdbus-1-dev libglib2.0-dev libsbc-dev python3 ca-certificates
WORK=$(mktemp -d /var/tmp/openaudiohub-bluealsa.XXXXXX)
trap 'rm -rf -- "$WORK"' EXIT
mkdir "$WORK/src"
if [[ -n "$SOURCE" ]]; then
  [[ -d "$SOURCE/.git" ]] || { echo 'The supplied BlueALSA source is not a Git checkout.' >&2; exit 1; }
  git -c safe.directory="$SOURCE" -C "$SOURCE" cat-file -e "$PIN^{commit}"
  git -c safe.directory="$SOURCE" -C "$SOURCE" archive "$PIN" | tar -x -C "$WORK/src"
else
  git init -q "$WORK/repo"
  git -C "$WORK/repo" remote add origin https://github.com/arkq/bluez-alsa.git
  timeout --kill-after=5s 180s git -C "$WORK/repo" fetch --depth=1 origin "$PIN"
  [[ $(git -C "$WORK/repo" rev-parse FETCH_HEAD) == "$PIN" ]]
  git -C "$WORK/repo" archive "$PIN" | tar -x -C "$WORK/src"
fi
# Validate exact source blob before applying a new, sink-specific patch.
[[ $(git hash-object "$WORK/src/src/a2dp-sbc.c") == 6880ef3965b0d1d74def0878f75b536464e80338 ]] || {
  echo 'Pinned source validation failed; no installed binaries were changed.' >&2; exit 1;
}
# The patch and the installed binary are both derivative works of the pinned
# upstream source. MIT requires its copyright and permission notice to travel
# with them, so capture it now and install it beside the binary later.
UPSTREAM_LICENSE=""
for CANDIDATE in "$WORK/src"/COPYING* "$WORK/src"/LICENSE* "$WORK/src"/LICENCE*; do
  if [[ -f "$CANDIDATE" ]]; then UPSTREAM_LICENSE="$CANDIDATE"; break; fi
done
[[ -n "$UPSTREAM_LICENSE" ]] || {
  echo 'Upstream COPYING/LICENSE not found in the pinned source; refusing to install an unattributed binary.' >&2; exit 1;
}
python3 "$ROOT/patches/bluealsa-receiver.py" "$WORK/src"
# Build unprivileged, from a clean source export. No code from the dirty worktree runs.
BUILD_USER="${OPENAUDIOHUB_BUILD_USER:-${SUDO_USER:-nobody}}"
id "$BUILD_USER" >/dev/null
chown -R "$BUILD_USER" "$WORK"
runuser -u "$BUILD_USER" -- bash -c '
  set -euo pipefail
  cd "$1/src"
  mkdir -p m4
  autoreconf --install --force
  mkdir build; cd build
  ../configure --prefix=/usr --sysconfdir=/etc --localstatedir=/var \
    --disable-systemd --disable-manpages --enable-debug
  make -j2
  ./src/bluealsa --version
' build "$WORK"
BIN="$WORK/src/build/src/bluealsa"
# Do not install a libtool shell wrapper instead of the executable.
[[ $(head -c4 "$BIN" | od -An -tx1 | tr -d ' \n') == 7f454c46 ]] || {
  echo 'Unexpected binary format; not installing.' >&2; exit 1;
}
install -d -m 0755 "$(dirname "$DEST")"
install -m 0755 "$BIN" "$DEST.new"
mv -f "$DEST.new" "$DEST"
printf '%s\n' "$PIN" > "$DEST.commit"
sha256sum "$ROOT/patches/bluealsa-receiver.py" | cut -d' ' -f1 > "$DEST.patch"
chmod 0644 "$DEST.commit" "$DEST.patch"
# Ship the upstream notice next to the binary and in the shared doc directory.
install -m 0644 "$UPSTREAM_LICENSE" "$DEST.COPYING"
install -d -m 0755 /usr/local/share/doc/openaudiohub
install -m 0644 "$UPSTREAM_LICENSE" /usr/local/share/doc/openaudiohub/bluealsa-4.3.1-COPYING
printf 'Source: https://github.com/arkq/bluez-alsa at %s\nPatch: patches/bluealsa-receiver.py\n' "$PIN" \
  > /usr/local/share/doc/openaudiohub/bluealsa-4.3.1-PROVENANCE
echo "Isolated receiver built: $DEST (upstream notice installed)"
