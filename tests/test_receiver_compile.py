#!/usr/bin/env python3
"""Compile-check the injected BlueALSA receiver C against local stubs.

The full BlueALSA build needs autotools, libbluetooth and GLib development
packages, which are not available on the packaging host. That makes the real
build unverifiable off-target, and a syntax or type error in the injected block
would only surface as a failed install on the appliance.

This test closes that gap: it compiles the exact injected C text (the same string
`patches/bluealsa-receiver.py` splices into the pinned source) against minimal
stubs for the BlueALSA and GLib symbols it touches. It proves the injected code
compiles; it does not prove linkage or runtime behaviour against real BlueALSA.
"""
import importlib.util
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

spec = importlib.util.spec_from_file_location('patcher', ROOT / 'patches/bluealsa-receiver.py')
patcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patcher)

STUBS = r'''
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

/* Minimal stand-ins for the BlueALSA and GLib symbols the injected block uses.
 * Field names and shapes mirror src/a2dp-sbc.c and src/ba-transport.h. */
struct sbc_caps { unsigned short max_bitpool; };
struct sep_caps { struct sbc_caps sbc; };
struct sep_cfg { struct sep_caps capabilities; };
struct a2dp_sep { struct sep_cfg config; };
struct ba_transport { const char *bluez_dbus_owner; const char *bluez_dbus_path; };

/* GLib's GError in the shape the injected code relies on: the message field is
 * read when a report is rejected. */
typedef struct { unsigned int domain; int code; char *message; } GError;
typedef struct { unsigned short value; } GVariant;
typedef struct { int fd; } ba_dbus;

static ba_dbus g_stub_dbus;
static struct { ba_dbus *dbus; } config = { &g_stub_dbus };

static const char *BLUEZ_IFACE_MEDIA_TRANSPORT = "org.bluez.MediaTransport1";

int g_dbus_set_property(ba_dbus *dbus, const char *owner, const char *path,
                        const char *iface, const char *property, GVariant *value,
                        GError **error);
GVariant *g_variant_new_uint16(unsigned short value);
void g_error_free(GError *error);

void oah_stub_log(const char *fmt, ...);
#define error(...) oah_stub_log(__VA_ARGS__)
#define warn(...) oah_stub_log(__VA_ARGS__)
#define info(...) oah_stub_log(__VA_ARGS__)
'''

# The exact text the patch splices in, compiled as-is.
INJECTED = patcher.INIT

HARNESS = r'''
/* Exercise both entry points so nothing is dead code in the compile check. */
static int a2dp_sbc_sink_transport_start(struct ba_transport *t) {
    oah_report_rendering_delay(t);
    return 0;
}
struct a2dp_sep a2dp_sbc_sink = {
    .config = { .capabilities = { .sbc = { .max_bitpool = 250 } } },
};

int main(void) {
    struct ba_transport t = { .bluez_dbus_owner = ":1.7", .bluez_dbus_path = "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF/a2dpsnk/source" };
    struct a2dp_sep sep = { 0 };
    if (oah_sbc_sink_init(&sep) != 0) return 1;
    return a2dp_sbc_sink_transport_start(&t);
}
'''

COMPILERS = ('cc', 'clang', 'gcc')


@unittest.skipUnless(any(shutil.which(c) for c in COMPILERS), 'no C compiler available')
class ReceiverPatchCompiles(unittest.TestCase):
    def test_injected_block_compiles_cleanly(self):
        cc = next(shutil.which(c) for c in COMPILERS if shutil.which(c))
        with tempfile.TemporaryDirectory() as d:
            src = Path(d) / 'injected.c'
            src.write_text(STUBS + '\n' + INJECTED + '\n' + HARNESS)
            for extra in (['-std=c11', '-Wall', '-Wextra', '-Werror'], ['-std=gnu99', '-Wall']):
                r = subprocess.run([cc, *extra, '-fsyntax-only', str(src)],
                                   capture_output=True, text=True)
                self.assertEqual(
                    r.returncode, 0,
                    f'{cc} {" ".join(extra)} failed:\n{r.stdout}{r.stderr}')

    def test_injected_block_records_delay_outcome(self):
        # The control plane distinguishes reported from rejected, so the injected
        # code must record both outcomes from the owning connection.
        self.assertIn('oah_write_delay_state(t->bluez_dbus_path, ms, "reported", NULL)', INJECTED)
        self.assertIn('oah_write_delay_state(t->bluez_dbus_path, ms, "rejected", err->message)', INJECTED)
        self.assertIn('g_variant_new_uint16(ms * 10)', INJECTED)
        # The write must be attempted from the acquired transport's owner, not a
        # separate client: BlueZ rejects the latter.
        self.assertIn('t->bluez_dbus_owner', INJECTED)
        self.assertIn('OAH_DELAY_STATE_FILE', INJECTED)

    def test_state_file_write_is_best_effort(self):
        # A missing or unwritable path must never disturb audio.
        self.assertIn('if (path == NULL || *path == \'\\0\') return;', INJECTED)
        self.assertIn('if (f == NULL) return;', INJECTED)


if __name__ == '__main__':
    unittest.main(verbosity=2)
