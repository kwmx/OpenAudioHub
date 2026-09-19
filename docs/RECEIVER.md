# Secondary receiver implementation and source provenance

Upstream: https://github.com/arkq/bluez-alsa, release v4.3.1.
Pinned commit: `f11569451b98a765adf9abc081632bb013f89f57`.
Original `src/a2dp-sbc.c` Git blob: `6880ef3965b0d1d74def0878f75b536464e80338`.
Upstream license: MIT. The installed binary and the patch are derivative works of
upstream, so `scripts/build-bluealsa.sh` refuses to install unless it finds the
upstream COPYING/LICENSE in the pinned source, then installs it next to the binary
(`<binary>.COPYING`) and under `/usr/local/share/doc/openaudiohub/`.

`patches/bluealsa-receiver.py` adds a sink initializer reading
`OAH_SBC_MAX_BITPOOL` before BlueZ endpoint registration. It changes the sink's
capability struct, not the shared `SBC_MAX_BITPOOL` constant. The A2DP source
encoder remains unchanged. Existing negotiated transports need reconnection.

The managed wrapper validates caps against 35,53,64,250 and reads only public
`audio.json`. The same C daemon binary handles all supported caps; no recompilation
is needed when changing the setting in the dashboard.

The optional `OAH_ADVERTISED_DELAY_MS` requests a fixed total rendering delay from
BlueALSA's owning D-Bus connection at sink transport start. BlueZ's MediaTransport1
Delay is an optional writable sink property, unit 0.1 ms, restricted to the
connection which acquired the transport. This is different from BlueALSA's local
PCM DelayAdjustment metadata. Rejection is logged and does not abort decoding.
No upstream-report success or physical synchronization is inferred by the UI.

Official API reference:
https://manpages.debian.org/trixie/bluez/org.bluez.MediaTransport.5.en.html

Reproduce the native build:

```bash
sudo env OPENAUDIOHUB_BLUEALSA_SOURCE=/home/<user>/src/bluez-alsa-bp35 \
  bash scripts/build-bluealsa.sh
```

The helper exports the clean commit, validates its SBC source, runs the guarded
patch, compiles unprivileged with Autotools, checks the ELF header, installs the
upstream license notice and installs only the isolated daemon. It does not run
upstream `make install` or overwrite Debian's BlueALSA package. The complete
native build is **not** validated in the environment that prepared this release:
verify it before production use.
