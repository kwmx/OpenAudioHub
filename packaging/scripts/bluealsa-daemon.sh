#!/bin/bash
set -euo pipefail
VALUES=$(/usr/local/lib/openaudiohub/audio-values.py) || exit 78
mapfile -t V <<< "$VALUES"
[[ ${#V[@]} -eq 4 ]] || exit 78
export OAH_SBC_MAX_BITPOOL="${V[2]}"
export OAH_ADVERTISED_DELAY_MS="${V[3]}"
BIN=/usr/local/lib/openaudiohub/bluealsa-4.3.1-oah
[[ -x "$BIN" ]] || { echo 'Missing isolated receiver; run scripts/build-bluealsa.sh.' >&2; exit 78; }
echo "Secondary SBC maximum=$OAH_SBC_MAX_BITPOOL; fixed rendering delay=$OAH_ADVERTISED_DELAY_MS ms (0=default)" >&2
exec "$BIN" --device=hci0 --profile=a2dp-sink --codec=SBC
