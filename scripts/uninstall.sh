#!/bin/bash
set -euo pipefail
if [[ ${EUID:-$(id -u)} -ne 0 ]]; then exec sudo bash "$0" "$@"; fi
systemctl disable --now openaudiohubd openaudiohub-audio-tuning openaudiohub-bluealsa-bridge openaudiohub-input2-bridge openaudiohub-bluealsa 2>/dev/null || true
rm -f /etc/systemd/system/openaudiohub*.service
systemctl daemon-reload
rm -f /usr/local/bin/openaudiohubd
rm -rf /usr/local/lib/openaudiohub /usr/share/openaudiohub /usr/local/share/doc/openaudiohub
printf 'OpenAudioHub services and binaries removed. Configuration remains in /etc/openaudiohub and Bluetooth pairings are untouched.\n'
