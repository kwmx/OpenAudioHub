> 1.0.2 additions: see RECEIVER.md and VALIDATION.md. Older architecture notes below are retained for context.

# OpenAudioHub architecture

## Audio data plane

OpenAudioHub intentionally separates the data plane from the control plane.

### Input 1

A2DP source device → BlueZ → PipeWire/WirePlumber A2DP Sink SEP → PipeWire playback stream.

### Input 2

A2DP source device → BlueZ → BlueALSA A2DP Sink SEP → persistent `bluealsa-aplay` → ALSA `default` (`pipewire-alsa`) → PipeWire playback stream.

### Output 1

PipeWire mix → WirePlumber A2DP Source role → BlueZ → headset/speaker.

At cold establishment (and explicit input-role changes), the daemon reproduces the proven manual order: connect the primary source while only PipeWire's sink SEP exists, start BlueALSA and its persistent bridge, then connect the secondary source. Once healthy, periodic reconciliation never tears down established AVDTP sessions.

## Control plane

`openaudiohubd` runs as a local root system service because it must coordinate:

- BlueZ connections
- system services
- hostname
- Bluetooth identity
- Netplan
- user PipeWire services

The web UI is authenticated by one shared hub password and talks only to `openaudiohubd`.

## Why SSE

Most live traffic is hub → browser: device changes, connections, transport state, health and eventually meters. SSE is smaller and simpler than a WebSocket control protocol; actions remain normal POST requests.

## RF policy

The reference image should default to / strongly prefer 5 GHz Wi-Fi. The controller still carries three Bluetooth audio transports and should not additionally carry ordinary Wi-Fi traffic in the same 2.4 GHz spectrum if it can be avoided.

## Volume domains

OpenAudioHub deliberately keeps two control domains separate:

- **Bluetooth transport volume** belongs to the device node card and uses the active BlueZ media transport. It reflects/controls the remote Bluetooth volume domain when the device exposes it.
- **Mixer gain** belongs to the center PipeWire mixer. Per-input gain, channel placement, mute, headroom and master gain are local digital processing and never rewrite the Bluetooth transport volume.

## Endpoint capacity

OpenAudioHub exposes two A2DP sink SEPs (PipeWire + BlueALSA), so two source devices may be connected simultaneously. An attempted third A2DP source can legitimately receive BlueZ `EBUSY` / `Device or resource busy`. OpenAudioHub removes stale unassigned A2DP transports during reconciliation so they cannot consume a slot invisibly.
