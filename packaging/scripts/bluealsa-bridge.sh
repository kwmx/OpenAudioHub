#!/bin/bash
set -euo pipefail
# A single persistent player. Never read root-only config.json.
VALUES=$(/usr/local/lib/openaudiohub/audio-values.py) || exit 78
mapfile -t V <<< "$VALUES"
[[ ${#V[@]} -eq 4 ]] || exit 78
: "${XDG_RUNTIME_DIR:?Missing audio-user runtime directory}"
exec 9>"$XDG_RUNTIME_DIR/openaudiohub-bluealsa-player.lock"
flock -n 9 || { echo 'Another OpenAudioHub secondary bridge holds the lock.' >&2; exit 73; }
if pgrep -x bluealsa-aplay >/dev/null; then
  echo 'Another bluealsa-aplay is already running. Stop the legacy/manual player first.' >&2
  exit 73
fi
ARGS=(--volume=software)
if [[ "${V[0]}" != 100000 || "${V[1]}" != 500000 ]]; then
  ARGS+=(--pcm-period-time="${V[0]}" --pcm-buffer-time="${V[1]}")
fi
export PIPEWIRE_ALSA='{ node.name=openaudiohub.bluealsa application.name=OpenAudioHub-BlueALSA }'
echo "Secondary bridge: ALSA default -> PipeWire, period=${V[0]}us buffer=${V[1]}us" >&2
exec /usr/bin/bluealsa-aplay "${ARGS[@]}"
