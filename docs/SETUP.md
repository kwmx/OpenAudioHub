# Setup / upgrade — OpenAudioHub 0.1.6-rc1

This is an integration candidate, not a hardware-qualified appliance image.
Read `VALIDATION.md` and complete the acceptance checks below before relying on
it. Keep any existing working setup until the cold-boot and playback checks pass.

## 1. Extract on the Orange Pi

Use a **new folder** rather than mixing files from previous versions:

```bash
mkdir -p ~/openaudiohub-upgrades/0.1.6-rc1
unzip OpenAudioHub-0.1.6-rc1.zip -d ~/openaudiohub-upgrades/0.1.6-rc1
cd ~/openaudiohub-upgrades/0.1.6-rc1/OpenAudioHub
```

The ZIP contains the full source, scripts, frontend, test report, and ARM64/AMD64
Go binaries. Do not overwrite your hand-patched BlueALSA directory.

## 2. Install

Run as your normal SSH user, invoking sudo only for installation:

```bash
sudo bash scripts/install.sh
```

On your existing setup the installer automatically finds:

```text
/home/<user>/src/bluez-alsa-bp35
```

It exports the clean pinned upstream commit from that checkout and builds a new,
separate sink-specific patch. It **does not reuse your globally modified source**.
No existing working-tree changes are lost.

For a differently located checkout:

```bash
sudo env OPENAUDIOHUB_BLUEALSA_SOURCE=/absolute/path/to/bluez-alsa-checkout \
  bash scripts/install.sh
```

Without a local checkout, the helper fetches the pinned Git commit from upstream.
Package download/build requirements need network access. The first build can take
several minutes on the 1 GB Pi and uses two compiler jobs; later identical builds
are skipped. The service upgrade is not started if the build fails.

The isolated daemon is installed at:

```text
/usr/local/lib/openaudiohub/bluealsa-4.3.1-oah
```

The stock `/usr/bin/bluealsa` is unchanged. **Do not run `make install`.**

After a successful preflight build the installer stops OpenAudioHub-owned audio
services and manual BlueALSA processes, removes obsolete user bridge units,
backs up the old private config under `/var/lib/openaudiohub/upgrade-backups/`,
and installs the updated services. Audio is interrupted during this stage.
Existing Wi-Fi, Bluetooth bonds, hostname, password and assigned roles are
preserved by the migration logic. This migration has not been exercised on a real
board yet.

If a process refuses to exit, installation stops with an error instead of starting
another player. Do not run parallel installers or manual BlueALSA players.

## 3. Check services and permissions

```bash
/usr/local/bin/openaudiohubd --version
sudo systemctl status openaudiohubd --no-pager
curl --max-time 5 http://127.0.0.1/api/health
pgrep -af 'bluealsa|bluealsa-aplay'
systemctl --user status openaudiohub-bluealsa-aplay.service --no-pager
sudo stat -c '%a %U:%G %n' /etc/openaudiohub/{config,audio}.json
```

Expected daemon version: `0.1.6-rc1`. The legacy **user** `bluealsa-aplay` service
should be absent/inactive. The managed player may wait for an assigned receiver.
`config.json` must stay `600 root:root`; `audio.json` is the non-secret `644`
projection used by the unprivileged bridge. Do not chmod the private file to 644.

Managed services:

```bash
sudo systemctl status openaudiohub-bluealsa openaudiohub-bluealsa-bridge --no-pager
sudo journalctl -u openaudiohub-bluealsa -u openaudiohub-bluealsa-bridge -n 80 --no-pager
```

The secondary endpoint is staged by OpenAudioHub; it is not independently enabled
for boot. A missing/failed endpoint must be diagnosed, not hidden by launching a
manual `bluealsa-aplay` alongside it.

## 4. Configure the Mac receiver

For the current hybrid topology use Phone → Input 1 / PipeWire, Mac → Input 2 /
BlueALSA, and headphones → Output 1. Confirm the actual backend in Diagnostics.

Open **Audio → Secondary receiver** or expand the BlueALSA input card's
**Receiver settings**. SBC maximum options are 35, 53, 64 and 250. Start at **35**.
The control describes a **receiver-wide limit**. It is not a separate cap for
both the PipeWire input and the BlueALSA input.

Saving a new cap reconnects the secondary source to renegotiate capabilities.
Do not judge it by the saved value alone. With the Mac connected, verify:

```bash
sudo gdbus call --system --dest org.bluealsa \
  --object-path /org/bluealsa/hci0/dev_00_00_5E_00_53_01/a2dpsnk/source \
  --method org.freedesktop.DBus.Properties.Get \
  org.bluealsa.PCM1 CodecConfiguration
```

For the known 44.1 kHz joint-stereo configuration and max 35 this should be
`21 15 02 23`. The final byte `23` is hexadecimal 35. The decoded **frame** bitpool
may be lower than the negotiated maximum; these are distinct quantities.
A disconnected Mac has no PCM object, so that query cannot succeed then.

## 5. A/V synchronization (experimental)

Leave **Fixed total rendering-delay report** at **0** until playback is stable.
Zero preserves the existing engine behavior. A nonzero value requests one fixed
total delay report from the owning BlueALSA D-Bus connection when a transport
starts. It is expressed in milliseconds in the UI and converted to 0.1 ms units
for BlueZ. Reconnect is required.

This controls the **reported total rendering latency**, not an additional amount
of audio to buffer. Do not enter a residual 50 ms offset as a total when the real
path is hundreds of milliseconds. Measure or calibrate the full path using the
same video player and headphones; the source must support and honor reports.

No automatic measurement, downstream headphone-latency discovery, physical clock
synchronization, or guaranteed correction is implemented. A successful D-Bus
request is not proof of correct lip-sync. BlueZ may reject a report when ownership
or delay-reporting support is absent; inspect the receiver journal. Return to 0
and reconnect to disable this experiment.

## 6. On-device acceptance checks

1. Verify only the intended BlueALSA daemon/player own the secondary PCM.
2. Play both inputs for at least 30 minutes; record any underruns or RTP loss.
3. Change a cap and verify the negotiated value after reconnect.
4. Open a dropdown through several telemetry updates; drag a slider and edit a
   settings field. Controls must not reset. Failed saves retain a retry action.
5. Disconnect a source from the source device; check that the other keeps playing.
6. Reboot only after the running test passes; confirm web/audio services return.
7. Calibrate A/V reporting separately. Do not trade an unexplained dropout for a
   larger reporting value.

The UI is served locally at `http://openaudiohub.local/` after services are healthy.
Reload the page after upgrading. Source files are served with `Cache-Control:
no-cache` so the browser revalidates the application scripts.

## Recovery

Do not rerun the old broken installer merely to recover a manual experiment.
Stop only the managed secondary stack and web supervisor before manual work:

```bash
sudo systemctl stop openaudiohubd openaudiohub-bluealsa-bridge openaudiohub-bluealsa
pgrep -af 'bluealsa|bluealsa-aplay' || true
```

PipeWire and Bluetooth are separate services. Your pre-existing stock and bp35
test binaries remain available. Never start a second BlueALSA daemon/player while
another owns `org.bluealsa` or the PCM. Preserve the backup and inspect logs on
failure; the upgrade is not a full OS rollback mechanism.
