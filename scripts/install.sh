#!/bin/bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  exec sudo -E bash "$0" "$@"
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="1.0.0"
exec 9>/run/lock/openaudiohub-install.lock
flock -n 9 || { echo 'Another OpenAudioHub install is running.' >&2; exit 1; }


RECOVERY_ARMED=0
on_exit() {
  rc=$?
  if [[ $rc -ne 0 && $RECOVERY_ARMED -eq 1 ]]; then
    echo >&2
    echo "Installer stopped early (exit $rc). Attempting to leave the control plane reachable..." >&2
    systemctl daemon-reload >/dev/null 2>&1 || true
    timeout --foreground --kill-after=2s 10s systemctl start openaudiohubd.service >/dev/null 2>&1 || true
  fi
  exit $rc
}
trap on_exit EXIT

pick_user() {
  if [[ -n "${OPENAUDIOHUB_USER:-}" ]]; then echo "$OPENAUDIOHUB_USER"; return; fi
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != root ]]; then echo "$SUDO_USER"; return; fi
  awk -F: '$3 >= 1000 && $3 < 60000 && $7 !~ /(nologin|false)$/ { print $1; exit }' /etc/passwd
}
AUDIO_USER="$(pick_user)"
if [[ -z "$AUDIO_USER" ]] || ! id "$AUDIO_USER" >/dev/null 2>&1; then
  echo "Could not determine the normal audio user. Re-run as: OPENAUDIOHUB_USER=<user> sudo -E ./scripts/install.sh" >&2
  exit 1
fi
AUDIO_UID="$(id -u "$AUDIO_USER")"
AUDIO_HOME="$(getent passwd "$AUDIO_USER" | cut -d: -f6)"
ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"
case "$ARCH" in arm64|aarch64|amd64|x86_64) ;; *) echo "Unsupported architecture: $ARCH" >&2; exit 1;; esac

echo "== OpenAudioHub $VERSION installer =="
echo "Audio user: $AUDIO_USER (uid $AUDIO_UID)"
echo "Home:       $AUDIO_HOME"
echo "Arch:       $ARCH"

echo "[1/9] Installing runtime packages..."
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  bluez bluez-alsa-utils \
  pipewire pipewire-bin pipewire-pulse pipewire-alsa wireplumber libspa-0.2-bluetooth \
  rtkit avahi-daemon alsa-utils dbus-user-session libpam-systemd \
  python3 python3-yaml python3-dbus python3-gi iw netplan.io rfkill ca-certificates util-linux procps libglib2.0-bin

# Compile the pinned, isolated receiver before touching the working services.
# The old bp35 Git checkout may be used as an offline source, but its dirty
# worktree is never copied: build-bluealsa.sh exports the verified commit.
PATCH_ID="$(sha256sum "$ROOT_DIR/patches/bluealsa-receiver.py" | cut -d' ' -f1)"
RECEIVER=/usr/local/lib/openaudiohub/bluealsa-4.3.1-oah
if [[ ! -x "$RECEIVER" ]] || [[ "$(cat "$RECEIVER.patch" 2>/dev/null || true)" != "$PATCH_ID" ]]; then
  if [[ -z "${OPENAUDIOHUB_BLUEALSA_SOURCE:-}" && -d "$AUDIO_HOME/src/bluez-alsa-bp35/.git" ]]; then
    export OPENAUDIOHUB_BLUEALSA_SOURCE="$AUDIO_HOME/src/bluez-alsa-bp35"
  fi
  OPENAUDIOHUB_BUILD_USER="$AUDIO_USER" bash "$ROOT_DIR/scripts/build-bluealsa.sh"
fi
BACKUP="/var/lib/openaudiohub/upgrade-backups/$(date -u +%Y%m%dT%H%M%SZ)-$VERSION"
install -d -m 0700 "$BACKUP"
[[ ! -f /etc/openaudiohub/config.json ]] || cp -p /etc/openaudiohub/config.json "$BACKUP/config.json"

echo "[1b/9] Quiescing previous OpenAudioHub services..."
# Upgrades used to modify BlueZ/PipeWire while the previous daemon was still
# reconciling routes. That creates exactly the sort of endpoint/process races an
# appliance installer must avoid. Stop only OpenAudioHub-owned services; do not
# restart Bluetooth or networking. Every stop is bounded so SSH cannot be held
# hostage by a wedged audio process.
timeout --foreground --kill-after=2s 15s systemctl stop \
  openaudiohubd.service \
  openaudiohub-bluealsa-bridge.service \
  openaudiohub-bluealsa.service \
  openaudiohub-bt-agent.service \
  openaudiohub-audio-tuning.service >/dev/null 2>&1 || true
RECOVERY_ARMED=1
for UNIT in openaudiohub-bluealsa-aplay.service openaudiohub-input2-bridge.service openaudiohub-audio-tuning.service; do
  timeout --foreground --kill-after=2s 8s runuser -u "$AUDIO_USER" -- env \
    HOME="$AUDIO_HOME" XDG_RUNTIME_DIR="/run/user/$AUDIO_UID" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$AUDIO_UID/bus" \
    systemctl --user disable --now "$UNIT" >/dev/null 2>&1 || true
  FILE="$AUDIO_HOME/.config/systemd/user/$UNIT"
  if [[ -f "$FILE" ]]; then cp -p "$FILE" "$BACKUP/user-$UNIT"; rm -f "$FILE"; fi
  rm -f "$AUDIO_HOME/.config/systemd/user/default.target.wants/$UNIT"
done
timeout --foreground --kill-after=2s 8s runuser -u "$AUDIO_USER" -- env \
  XDG_RUNTIME_DIR="/run/user/$AUDIO_UID" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$AUDIO_UID/bus" \
  systemctl --user daemon-reload >/dev/null 2>&1 || true
pkill -x bluealsa-aplay >/dev/null 2>&1 || true
pkill -x bluealsa >/dev/null 2>&1 || true
sleep 1
if pgrep -x bluealsa-aplay >/dev/null || pgrep -x bluealsa >/dev/null; then
  echo 'A manual/legacy BlueALSA process is still running; stop it before retrying installation.' >&2
  exit 1
fi
RECOVERY_ARMED=1

echo "[2/9] Installing files..."
install -d -m 0755 /usr/local/lib/openaudiohub /usr/share/openaudiohub/web /etc/openaudiohub /var/lib/openaudiohub/netplan-backups /run/openaudiohub
install -m 0755 "$ROOT_DIR/packaging/scripts/audio-values.py" /usr/local/lib/openaudiohub/audio-values.py
install -m 0755 "$ROOT_DIR/packaging/scripts/bluealsa-daemon.sh" /usr/local/lib/openaudiohub/bluealsa-daemon.sh
install -m 0755 "$ROOT_DIR/packaging/scripts/bluealsa-bridge.sh" /usr/local/lib/openaudiohub/bluealsa-bridge.sh
install -m 0755 "$ROOT_DIR/packaging/scripts/apply-audio.sh" /usr/local/lib/openaudiohub/apply-audio.sh
install -m 0755 "$ROOT_DIR/packaging/scripts/netplan_wifi.py" /usr/local/lib/openaudiohub/netplan_wifi.py
install -m 0755 "$ROOT_DIR/packaging/scripts/bt_agent.py" /usr/local/lib/openaudiohub/bt_agent.py
install -m 0755 "$ROOT_DIR/packaging/scripts/collect-diagnostics.sh" /usr/local/bin/openaudiohub-diagnostics
install -m 0755 "$ROOT_DIR/scripts/update.sh" /usr/local/lib/openaudiohub/update.sh
rm -rf /usr/share/openaudiohub/web/*
cp -a "$ROOT_DIR/web/." /usr/share/openaudiohub/web/

BIN="$ROOT_DIR/release/openaudiohubd-linux-arm64"
case "$ARCH" in
  arm64|aarch64) BIN="$ROOT_DIR/release/openaudiohubd-linux-arm64" ;;
  amd64|x86_64) BIN="$ROOT_DIR/release/openaudiohubd-linux-amd64" ;;
esac
if [[ ! -x "$BIN" ]]; then
  if command -v go >/dev/null 2>&1; then
    echo "Bundled binary not found; building locally..."
    (cd "$ROOT_DIR" && go build -ldflags "-X main.version=$VERSION" -o /usr/local/bin/openaudiohubd ./cmd/openaudiohubd)
  else
    echo "No bundled binary for $ARCH and Go is unavailable." >&2
    exit 1
  fi
else
  install -m 0755 "$BIN" /usr/local/bin/openaudiohubd
fi

cat > /etc/openaudiohub/runtime.env <<EOF_RUNTIME
OAH_AUDIO_USER=$AUDIO_USER
OAH_AUDIO_UID=$AUDIO_UID
OAH_AUDIO_HOME=$AUDIO_HOME
EOF_RUNTIME
chmod 0644 /etc/openaudiohub/runtime.env

echo "[3/9] Initializing OpenAudioHub configuration..."
FRESH_CONFIG=0
if [[ ! -f /etc/openaudiohub/config.json ]]; then
  FRESH_CONFIG=1
  while true; do
    read -rsp "Choose OpenAudioHub web password (8+ chars): " PASS1; echo
    read -rsp "Repeat password: " PASS2; echo
    [[ "$PASS1" == "$PASS2" ]] || { echo "Passwords do not match."; continue; }
    [[ ${#PASS1} -ge 8 ]] || { echo "Password must be at least 8 characters."; continue; }
    break
  done
  printf '%s\n' "$PASS1" | OPENAUDIOHUB_AUDIO_USER="$AUDIO_USER" OPENAUDIOHUB_AUDIO_UID="$AUDIO_UID" \
    /usr/local/bin/openaudiohubd --init --config /etc/openaudiohub/config.json
  unset PASS1 PASS2
else
  python3 - "$AUDIO_USER" "$AUDIO_UID" <<'PY'
import json,sys
p='/etc/openaudiohub/config.json'
c=json.load(open(p)); c['audioUser']=sys.argv[1]; c['audioUid']=int(sys.argv[2])
json.dump(c,open(p,'w'),indent=2); open(p,'a').write('\n')
PY
  # v0.1.4 returns the Balanced BlueALSA path to the exact 4.3.1 defaults
  # (100 ms / 500 ms) used by the stable hand-tested prototype. Migrate only
  # OpenAudioHub stock values; user-custom timing remains untouched.
  python3 - <<'PY'
import json
p='/etc/openaudiohub/config.json'
c=json.load(open(p)); a=c.setdefault('audio',{})
changed=False
if a.get('preset') == 'balanced' and (a.get('bluealsaPeriodUs'),a.get('bluealsaBufferUs')) in {(75000,300000),(80000,320000)}:
    a['bluealsaPeriodUs']=100000; a['bluealsaBufferUs']=500000; changed=True
if a.get('preset') == 'stable' and (a.get('bluealsaPeriodUs'),a.get('bluealsaBufferUs')) in {(100000,400000),(100000,500000)}:
    a['bluealsaPeriodUs']=100000; a['bluealsaBufferUs']=500000; changed=True
if a.get('resampler') != 'auto':
    a['resampler']='auto'; changed=True
if a.get('liveMeters'):
    a['liveMeters']=False; changed=True
prefs=c.setdefault('devicePrefs',{})
for addr in c.get('slots',{}).get('inputs',[])+c.get('slots',{}).get('outputs',[]):
    if addr and addr not in prefs:
        prefs[addr]={'autoConnect': True}; changed=True
if changed:
    json.dump(c,open(p,'w'),indent=2); open(p,'a').write('\n')
PY
  chmod 0600 /etc/openaudiohub/config.json
  echo "Existing config preserved."
fi

python3 - <<'PYPROJ'
import json,os,tempfile
p='/etc/openaudiohub/config.json'
with open(p) as f:c=json.load(f)
a=c.setdefault('audio',{})
a.setdefault('secondarySbcMaxBitpool',35)
a.setdefault('secondaryAdvertisedDelayMs',0)
for target,data,mode in [(p,c,0o600),('/etc/openaudiohub/audio.json',a,0o644)]:
    fd,tmp=tempfile.mkstemp(prefix='.upgrade-',dir='/etc/openaudiohub')
    try:
        with os.fdopen(fd,'w') as f:
            os.fchmod(f.fileno(),mode);json.dump(data,f,indent=2);f.write('\n');f.flush();os.fsync(f.fileno())
        os.replace(tmp,target)
    finally:
        if os.path.exists(tmp):os.unlink(tmp)
PYPROJ
chmod 0755 /etc/openaudiohub
chmod 0600 /etc/openaudiohub/config.json
/usr/local/lib/openaudiohub/audio-values.py >/dev/null

echo "[4/9] Configuring Bluetooth identity and policy..."
BT_NAME="$(python3 - <<'PY'
import json
try:
    print(json.load(open('/etc/openaudiohub/config.json')).get('bluetoothName') or 'OpenAudioHub')
except Exception:
    print('OpenAudioHub')
PY
)"
OPENAUDIOHUB_BT_NAME="$BT_NAME" python3 - <<'PY'
from pathlib import Path
import os,re
p=Path('/etc/bluetooth/main.conf')
s=p.read_text() if p.exists() else ''
if '[General]' not in s: s='[General]\n'+s
for key,val in [('Name',os.environ.get('OPENAUDIOHUB_BT_NAME','OpenAudioHub')),('Class','0x200414')]:
    pat=re.compile(rf'(?m)^\s*#?\s*{re.escape(key)}\s*=.*$')
    if pat.search(s): s=pat.sub(f'{key} = {val}',s,1)
    else: s=s.replace('[General]',f'[General]\n{key} = {val}',1)
if '[Policy]' not in s: s += '\n[Policy]\n'
pat=re.compile(r'(?m)^\s*#?\s*AutoEnable\s*=.*$')
if pat.search(s): s=pat.sub('AutoEnable=true',s,1)
else: s=s.replace('[Policy]','[Policy]\nAutoEnable=true',1)
p.write_text(s)
PY

# Set the default appliance hostname only on first install. Upgrades preserve UI-edited identity.
if [[ "$FRESH_CONFIG" == 1 ]]; then
  hostnamectl set-hostname openaudiohub || true
fi
rfkill unblock bluetooth || true

echo "[5/9] Preparing headless PipeWire/WirePlumber session..."
loginctl enable-linger "$AUDIO_USER"
timeout --foreground --kill-after=2s 10s systemctl start "user@$AUDIO_UID.service" || true
usermod -aG audio "$AUDIO_USER" || true
timeout --foreground --kill-after=2s 8s runuser -u "$AUDIO_USER" -- env XDG_RUNTIME_DIR="/run/user/$AUDIO_UID" systemctl --user mask pulseaudio.service pulseaudio.socket >/dev/null 2>&1 || true
# Retire the experimental config created during manual prototyping, if present.
OLD_WP="$AUDIO_HOME/.config/wireplumber/wireplumber.conf.d/51-bt-hub.conf"
if [[ -f "$OLD_WP" ]]; then
  mv "$OLD_WP" "$OLD_WP.pre-openaudiohub"
  chown "$AUDIO_USER:$AUDIO_USER" "$OLD_WP.pre-openaudiohub"
fi
timeout --foreground --kill-after=2s 12s runuser -u "$AUDIO_USER" -- env XDG_RUNTIME_DIR="/run/user/$AUDIO_UID" systemctl --user enable --now pipewire.socket pipewire-pulse.socket wireplumber.service || true

# Disable distro BlueALSA auto-start. OpenAudioHub owns the second endpoint lifecycle.
timeout --foreground --kill-after=2s 10s systemctl disable --now bluealsa.service bluealsa-aplay.service >/dev/null 2>&1 || true

echo "[6/9] Installing systemd units..."
install -m 0644 "$ROOT_DIR/packaging/systemd/openaudiohub-bluealsa.service" /etc/systemd/system/openaudiohub-bluealsa.service
install -m 0644 "$ROOT_DIR/packaging/systemd/openaudiohub-bt-agent.service" /etc/systemd/system/openaudiohub-bt-agent.service
install -m 0644 "$ROOT_DIR/packaging/systemd/openaudiohubd.service" /etc/systemd/system/openaudiohubd.service
# Retire the per-device bridge from 0.1.0-0.1.3. It could restart-loop when a
# source disconnected and was a major source of instability.
timeout --foreground --kill-after=2s 8s systemctl disable --now openaudiohub-input2-bridge.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/openaudiohub-input2-bridge.service /usr/local/lib/openaudiohub/input2-bridge.sh
sed -e "s/__AUDIO_USER__/$AUDIO_USER/g" -e "s/__AUDIO_UID__/$AUDIO_UID/g" -e "s#__AUDIO_HOME__#$AUDIO_HOME#g" \
  "$ROOT_DIR/packaging/systemd/openaudiohub-bluealsa-bridge.service.in" > /etc/systemd/system/openaudiohub-bluealsa-bridge.service
sed -e "s/__AUDIO_USER__/$AUDIO_USER/g" -e "s/__AUDIO_UID__/$AUDIO_UID/g" -e "s#__AUDIO_HOME__#$AUDIO_HOME#g" \
  "$ROOT_DIR/packaging/systemd/openaudiohub-audio-tuning.service.in" > /etc/systemd/system/openaudiohub-audio-tuning.service
systemctl daemon-reload

echo "[7/9] Applying audio configuration..."
# Never restart bluetooth.service during an SSH upgrade. The persistent identity
# is already written to main.conf; system-alias updates the live adapter without
# tearing down BlueZ or disturbing the combo radio.
timeout --foreground --kill-after=2s 10s systemctl enable --now bluetooth.service >/dev/null 2>&1 || true
timeout --foreground --kill-after=2s 8s bluetoothctl system-alias "$BT_NAME" >/dev/null 2>&1 || true
timeout --foreground --kill-after=2s 8s systemctl start rtkit-daemon >/dev/null 2>&1 || true
timeout --foreground --kill-after=2s 10s systemctl enable --now avahi-daemon >/dev/null 2>&1 || true

# BlueALSA is intentionally NOT enabled directly at boot. openaudiohubd stages the
# first source onto PipeWire, then starts BlueALSA's second SEP and bridge before
# connecting the second source.
systemctl enable openaudiohub-bt-agent.service openaudiohub-audio-tuning.service >/dev/null 2>&1 || true
systemctl disable openaudiohub-bluealsa.service openaudiohub-bluealsa-bridge.service >/dev/null 2>&1 || true
systemctl reset-failed openaudiohub-audio-tuning.service openaudiohub-bt-agent.service >/dev/null 2>&1 || true

# Apply the files and restart the user audio graph directly. This operation is
# strictly bounded. Even if a PipeWire/WirePlumber stop job is wedged, the
# installer proceeds and the daemon can heal the user graph after the web UI is
# back. apply-audio.sh writes configuration before touching the running graph.
if ! timeout --foreground --kill-after=3s 25s /usr/local/lib/openaudiohub/apply-audio.sh; then
  echo "WARNING: live audio restart did not finish within 25 seconds; configuration was installed and will be retried by OpenAudioHub/next boot." >&2
fi
timeout --foreground --kill-after=2s 10s systemctl restart openaudiohub-bt-agent.service >/dev/null 2>&1 || true

echo "[8/9] Enabling OpenAudioHub..."
systemctl enable openaudiohubd.service >/dev/null 2>&1 || true
systemctl reset-failed openaudiohubd.service >/dev/null 2>&1 || true
if ! timeout --foreground --kill-after=2s 12s systemctl restart openaudiohubd.service; then
  echo "WARNING: openaudiohubd did not report ready to systemd within 12 seconds; checking it asynchronously." >&2
  systemctl start --no-block openaudiohubd.service >/dev/null 2>&1 || true
fi
sleep 2

echo "[9/9] Quick health check..."
systemctl --no-pager --full status openaudiohubd.service | sed -n '1,12p' || true
if ! systemctl is-enabled --quiet openaudiohubd.service; then
  echo "WARNING: openaudiohubd.service is not enabled for boot." >&2
fi
if /usr/sbin/iw dev wlan0 link 2>/dev/null | grep -q 'freq: 24'; then
  echo
  echo "WARNING: wlan0 is on 2.4 GHz. OpenAudioHub works best on 5 GHz when mixing multiple Bluetooth streams."
fi

RECOVERY_ARMED=0
IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
echo "OpenAudioHub installed."
echo "  Web UI: http://openaudiohub.local/"
[[ -n "$IP" ]] && echo "  Web UI: http://$IP/"
echo "  Logs:   sudo journalctl -u openaudiohubd -f"
echo
echo "Existing Bluetooth pairings and Wi-Fi settings were preserved. Assign Input 1, Input 2 and Output 1 in the Devices screen."
