#!/bin/bash
set -u
OUT="${1:-/tmp/openaudiohub-diagnostics-$(date +%Y%m%d-%H%M%S).txt}"
exec > >(tee "$OUT") 2>&1

echo "OpenAudioHub diagnostics"
date -Is
uname -a
cat /etc/os-release 2>/dev/null | grep -E '^(PRETTY_NAME|VERSION_ID)=' || true
/usr/local/bin/openaudiohubd --version 2>/dev/null || true

echo; echo '=== services ==='
for s in bluetooth openaudiohubd openaudiohub-bt-agent openaudiohub-audio-tuning openaudiohub-bluealsa openaudiohub-bluealsa-bridge rtkit-daemon; do
  printf '%-34s ' "$s"
  systemctl is-active "$s" 2>/dev/null || true
done

echo; echo '=== bluetooth controller ==='
bluetoothctl show 2>&1 || true
echo; echo '=== connected devices ==='
bluetoothctl devices Connected 2>&1 || true
echo; echo '=== transports ==='
mapfile -t T < <(bluetoothctl transport.list 2>/dev/null | sed -n 's/^Transport //p')
for t in "${T[@]}"; do bluetoothctl transport.show "$t" 2>&1 || true; done

echo; echo '=== BlueALSA PCMs ==='
bluealsa-aplay -L 2>&1 || true

if [[ -f /etc/openaudiohub/runtime.env ]]; then
  # shellcheck disable=SC1091
  source /etc/openaudiohub/runtime.env
  audio() {
    runuser -u "$OAH_AUDIO_USER" -- env \
      HOME="$OAH_AUDIO_HOME" XDG_RUNTIME_DIR="/run/user/$OAH_AUDIO_UID" \
      DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$OAH_AUDIO_UID/bus" \
      PULSE_SERVER="unix:/run/user/$OAH_AUDIO_UID/pulse/native" "$@"
  }
  echo; echo '=== user audio services ==='
  audio systemctl --user is-active pipewire.service pipewire-pulse.service wireplumber.service 2>&1 || true
  echo; echo '=== sinks ==='; audio pactl list short sinks 2>&1 || true
  echo; echo '=== sink inputs ==='; audio pactl list short sink-inputs 2>&1 || true
  echo; echo '=== sources ==='; audio pactl list short sources 2>&1 || true
  echo; echo '=== pw-top ==='; audio pw-top -b -n 1 2>&1 || true
fi

echo; echo '=== wifi link (no credentials) ==='
/usr/sbin/iw dev wlan0 link 2>&1 || true

echo; echo '=== recent journal ==='
journalctl -b --no-pager -n 250 \
  -u bluetooth.service -u openaudiohubd.service -u openaudiohub-bluealsa.service \
  -u openaudiohub-bluealsa-bridge.service -u openaudiohub-audio-tuning.service 2>&1 || true

echo
echo "Saved to: $OUT"
