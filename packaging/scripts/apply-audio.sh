#!/bin/bash
set -euo pipefail
source /etc/openaudiohub/runtime.env

readarray -t V < <(python3 - <<'PY'
import json
c=json.load(open('/etc/openaudiohub/config.json'))
a=c.get('audio',{})
print(a.get('preferredRate',48000))
print(' '.join(map(str,a.get('allowedRates',[44100,48000]))))
print(a.get('quantum',2048))
print(a.get('codecPolicy','compatibility'))
PY
)
RATE="${V[0]}"; ALLOWED="${V[1]}"; QUANTUM="${V[2]}"; CODECS="${V[3]}"

PW_DIR="$OAH_AUDIO_HOME/.config/pipewire/pipewire.conf.d"
WP_DIR="$OAH_AUDIO_HOME/.config/wireplumber/wireplumber.conf.d"
install -d -o "$OAH_AUDIO_USER" -g "$OAH_AUDIO_USER" "$PW_DIR" "$WP_DIR"

cat > "$PW_DIR/90-openaudiohub.conf" <<CFG
context.properties = {
  default.clock.rate = $RATE
  default.clock.allowed-rates = [ $ALLOWED ]
  default.clock.quantum = $QUANTUM
  default.clock.min-quantum = 256
  default.clock.max-quantum = 8192
}
CFG
chown "$OAH_AUDIO_USER:$OAH_AUDIO_USER" "$PW_DIR/90-openaudiohub.conf"

if [[ "$CODECS" == "compatibility" ]]; then
  CODEC_LINE='bluez5.codecs = [ sbc ]'
else
  CODEC_LINE=''
fi
cat > "$WP_DIR/51-openaudiohub.conf" <<CFG
wireplumber.profiles = {
  main = {
    monitor.bluez.seat-monitoring = disabled
  }
}
monitor.bluez.properties = {
  bluez5.roles = [ a2dp_sink a2dp_source ]
  $CODEC_LINE
  bluez5.hfphsp-backend = "none"
}
monitor.bluez.rules = [
  {
    matches = [ { node.name = "~bluez_input.*" } ]
    actions = { update-props = { bluez5.media-source-role = "playback" } }
  }
]
CFG
chown "$OAH_AUDIO_USER:$OAH_AUDIO_USER" "$WP_DIR/51-openaudiohub.conf"

# Restart the user's audio graph. openaudiohubd reconnects assigned devices afterward.
AUDIO_ENV=(
  "HOME=$OAH_AUDIO_HOME"
  "XDG_RUNTIME_DIR=/run/user/$OAH_AUDIO_UID"
  "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$OAH_AUDIO_UID/bus"
  "PULSE_SERVER=unix:/run/user/$OAH_AUDIO_UID/pulse/native"
)
# Start first so the unit also recovers correctly after a cold boot where the
# socket-activated user graph has not been touched yet, then restart to apply
# the new configuration atomically.
timeout --foreground --kill-after=2s 8s runuser -u "$OAH_AUDIO_USER" -- env "${AUDIO_ENV[@]}" \
  systemctl --user start pipewire.socket pipewire-pulse.socket wireplumber.service || true
timeout --foreground --kill-after=2s 10s runuser -u "$OAH_AUDIO_USER" -- env "${AUDIO_ENV[@]}" \
  systemctl --user restart pipewire.service pipewire-pulse.service wireplumber.service || true
sleep 1
timeout --foreground --kill-after=1s 3s runuser -u "$OAH_AUDIO_USER" -- env "${AUDIO_ENV[@]}" \
  pw-metadata -n settings 0 clock.force-quantum "$QUANTUM" >/dev/null 2>&1 || true
