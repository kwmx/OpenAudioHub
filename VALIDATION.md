# OpenAudioHub 0.1.6-rc1 — validation report

Scope: what has actually been executed for this release, and what still has to be
proven on real hardware. Host-side checks passing is not the same as a
hardware-qualified appliance image.

## Completed checks

| Check | Result | Scope |
|---|---|---|
| `go test -v -timeout 60s ./...` | PASS — 16 tests | Regressions plus receiver bounds, config projection permissions/secrets, revision monotonicity and cached state |
| `go vet ./...` | PASS | Go static checks, exit 0 |
| `go test -race -timeout 90s ./...` | PASS | The available test suite, not a simulated physical appliance |
| `python3 tests/test_runtime.py` | PASS — 6 tests | Runtime option validation, secret/public read boundary using an unprivileged UID, legacy service migration checks and structural source-patch guards |
| `python3 tests/browser_regression.py` | PASS — 7 tests | Headless Chromium, offline, mocked fetch/SSE; dropdown identity, real range drag, unsaved text/audio edits, stale revisions, ordered writes, volume separation, failed-save retry, mobile overflow |
| Bash / Python / JavaScript syntax | PASS | Output in `tests/artifacts/syntax.log` |
| Linux ARM64 Go build | PASS | Cross-compiled; not executed on ARM in the packaging environment |
| Linux AMD64 Go build | PASS | Executed with `--version`, reports `0.1.6-rc1` |
| ZIP CRC and extraction checks | PASS | Verified on the delivered archive |

Notes on the browser tests: they load the real UI code and CSS offline and inject
controlled API/SSE responses, so they exercise the actual DOM controls and
interactions but not the HTTP transport or native Bluetooth. Loopback navigation
is unavailable in the packaging environment, so responses are injected in-page
rather than fetched. Commands are in `tests/artifacts/`.

## Not tested / release gates

- Complete native compilation and linking of the patched BlueALSA source. Upstream
  network access and codec development dependencies were unavailable during
  packaging, so the target-side build helper must pass before installation
  proceeds. The structural Python patch test is **not** a full C or integration
  build.
- Running `install.sh` against a real Armbian/Trixie board: systemd user sessions,
  legacy-service cleanup on that machine, and a cold boot.
- Actual SBC capabilities and selected configuration, and source/output
  reconnection.
- Physical Bluetooth source volume control, sustained concurrent playback, and
  missing RTP / underrun behaviour.
- BlueZ acceptance of the experimental owning-client delay report, source
  application behaviour, and measured A/V sync. Keep its default value of zero
  until independently tested and calibrated.
- Safari and Firefox native `<select>` behaviour. Browser automation used Chromium
  only.
- Exhaustive security, load, crash-recovery or installation-rollback testing.

## Interpretation

There are no known unresolved failures in the completed host-side checks.
**Untested hardware and build integration is not a passing result**: use 0.1.6-rc1
for controlled on-device acceptance, not as a proven-stable OS image. The
acceptance checklist is in `docs/SETUP.md`.

The configurable cap applies only to the secondary BlueALSA receiver. The fixed
rendering-delay field is a reported total, not extra buffering and not an
automatic calibration.
