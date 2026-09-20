# OpenAudioHub 1.0.1 validation report

Scope: what has actually been executed for this release, and what still has to be
proven on real hardware. Host-side checks passing is not the same as a
hardware-qualified appliance image.

## Completed checks

| Check | Result | Scope |
|---|---|---|
| `go test -v -timeout 60s ./...` | PASS: 47 tests | Regressions plus receiver bounds, config projection permissions/secrets, revision monotonicity, the delay-report state machine, Bluetooth connect backoff, input capacity, update version comparison and restore rejection |
| `go vet ./...` | PASS | Go static checks, exit 0 |
| `go test -race -timeout 90s ./...` | PASS | The available test suite, not a simulated physical appliance |
| `python3 tests/test_runtime.py` | PASS: 8 tests | Runtime option validation, delay passthrough and bounds, daemon exports, secret/public read boundary using an unprivileged UID, legacy service migration checks and structural source-patch guards |
| `python3 tests/test_receiver_compile.py` | PASS: 3 tests | Compiles the injected BlueALSA C against stubs with `-Wall -Wextra -Werror`; proves the injected block is valid C, not that it links or runs against real BlueALSA |
| `python3 tests/browser_regression.py` | PASS: 14 tests | Headless Chromium, offline, mocked fetch/SSE; dropdown identity, real range drag, unsaved text/audio edits, stale revisions, ordered writes, volume separation, failed-save retry, mobile overflow, A/V delay apply/reset and status, device grouping, discovered-device type, safe buffer combinations |
| Bash / Python / JavaScript syntax | PASS | Output in `tests/artifacts/syntax.log` |
| Linux ARM64 Go build | PASS | Cross-compiled; not executed on ARM in the packaging environment |
| Linux AMD64 Go build | PASS | Executed with `--version`, reports `1.0.1` |

Notes on the browser tests: they load the real UI code and CSS offline and inject
controlled API/SSE responses, so they exercise the actual DOM controls and
interactions but not the HTTP transport or native Bluetooth. Loopback navigation
is unavailable in the packaging environment, so responses are injected in-page
rather than fetched.

## Verified on real hardware

These were exercised on an Orange Pi Zero 2W running Armbian (Debian trixie,
aarch64) with a real phone, computer and two headsets.

- **Install and upgrade.** `scripts/install.sh` completes; a redeploy with an
  unchanged receiver patch correctly skips the BlueALSA rebuild.
- **Pairing.** A WH-1000XM3 previously reverted to `Paired: no / Trusted: yes` and
  kept asking to pair. Measured pairing time was 27 s against a 20 s timeout that
  killed the attempt mid-flight. With the timeout raised and the bond verified
  after pairing, pairing completes and holds.
- **A/V rendering-delay report.** Requesting 150 ms produced, from three
  independent sources: the state file written by the owning process
  (`requested_ms=150 result=reported`), the live BlueZ transport
  (`Delay: 0x05dc` = 150 ms), and the API (`state=reported`, actual 150 ms).
  Reset returned the transport to `Delay: 0`. The unit conversion holds.
- **Output switching.** With one A2DP source endpoint, connecting a second output
  releases the first and switches; the transport holder moved between headsets.
- **Bluetooth limits.** The engine registers one A2DP source endpoint, so exactly
  one Bluetooth output can be active at a time. Connecting a second output while
  the first holds it produces `Unable to select SEP`, which is reported to the user rather
  than retried forever.
- **SBC capacity.** Four local A2DP sink endpoints were observed (one from
  WirePlumber, three from BlueALSA), which is the basis for the input capacity.

## Not tested / release gates

- **More than two simultaneous sources.** The model supports four input slots and
  the staging loop is generalised, but only two sources have ever been connected at
  once. Three and four are unit- and render-tested only. UI labels them
  experimental for that reason.
- **Two simultaneous Bluetooth outputs.** Not possible with the current engine; it
  would need a second A2DP source endpoint.
- **The update install path.** The check, the refusal paths and the script's
  failure modes are verified, but no release had been published at the time of
  testing, so `update.sh install <tag>` has never been run end to end.
- Complete native compilation and linking of the patched BlueALSA source on a
  clean machine. The target-side build helper must pass before installation
  proceeds. The structural Python patch test is **not** a full C build.
- Cold boot, long concurrent playback, and missing RTP / underrun behaviour over
  time.
- Whether a source application actually honours the delay report. BlueZ accepting
  the report is not proof that video was corrected; only re-measuring shows that.
- Safari and Firefox native `<select>` behaviour. Browser automation used Chromium
  only.
- Exhaustive security, load, crash-recovery or installation-rollback testing.

## Interpretation

There are no known unresolved failures in the completed checks. **Untested
hardware and build integration is not a passing result.** The items listed above
should be exercised on the appliance before this is treated as stable, and the
on-device acceptance checklist in `docs/SETUP.md` remains the gate.

The configurable SBC cap applies only to the BlueALSA receiver, which serves every
input after the first. The fixed rendering-delay field is a reported total, not
extra buffering and not an automatic calibration.
