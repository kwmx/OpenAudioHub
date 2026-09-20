#!/usr/bin/env python3
"""Patch only the SBC *sink* in the pinned BlueALSA v4.3.1 source.

The shared SBC_MAX_BITPOOL constant and SBC source encoder remain unchanged.
This is not a protocol extension: it selects a lower advertised SBC maximum,
and optionally reports a fixed total rendering delay from the connection that
acquired the transport.

The delay report is made from this daemon because BlueZ restricts writes to
MediaTransport1.Delay to the connection holding the transport. BlueZ accepting
the write is not proof that the value stuck, and neither is proof that a source
application corrected its video, so the outcome is recorded for the control
plane to read and verify against the live transport property.
"""
from pathlib import Path
import argparse

MARKER = 'OpenAudioHub receiver controls v2'

INIT = r'''
/* OpenAudioHub receiver controls v2. These are read only at daemon startup. */
static int oah_env_int(const char *name, int def, int min, int max) {
    const char *value = getenv(name);
    if (value == NULL || *value == '\0') return def;
    char *end = NULL;
    errno = 0;
    long n = strtol(value, &end, 10);
    if (errno != 0 || end == value || *end != '\0' || n < min || n > max) {
        error("OpenAudioHub: invalid %s (expected %d..%d)", name, min, max);
        return -1;
    }
    return (int)n;
}

/* Record the outcome of a delay report so the control plane can verify it against
 * the live transport property. Only this daemon knows whether BlueZ accepted the
 * write, because only this daemon owns the transport. Best effort throughout: a
 * missing or unwritable path must never disturb audio. */
static void oah_write_delay_state(const char *transport, int ms, const char *result, const char *detail) {
    const char *path = getenv("OAH_DELAY_STATE_FILE");
    if (path == NULL || *path == '\0') return;
    FILE *f = fopen(path, "w");
    if (f == NULL) return;
    fprintf(f, "transport=%s\n", transport != NULL ? transport : "");
    fprintf(f, "requested_ms=%d\n", ms);
    fprintf(f, "result=%s\n", result);
    if (detail != NULL && *detail != '\0') {
        /* Keep the record one line per key so it stays trivially parseable. */
        fprintf(f, "detail=");
        for (const char *p = detail; *p != '\0'; p++)
            fputc((*p == '\n' || *p == '\r') ? ' ' : *p, f);
        fputc('\n', f);
    }
    fprintf(f, "epoch=%ld\n", (long)time(NULL));
    fclose(f);
}

static int oah_sbc_sink_init(struct a2dp_sep *sep) {
    int cap = oah_env_int("OAH_SBC_MAX_BITPOOL", 250, 2, 250);
    int delay = oah_env_int("OAH_ADVERTISED_DELAY_MS", 0, 0, 2000);
    if (cap < 0 || delay < 0) return -1;
    sep->config.capabilities.sbc.max_bitpool = cap;
    info("OpenAudioHub: SBC sink max bitpool=%d, fixed rendering delay=%d ms (0=default)", cap, delay);
    return 0;
}

static void oah_report_rendering_delay(struct ba_transport *t) {
    const int ms = oah_env_int("OAH_ADVERTISED_DELAY_MS", 0, 0, 2000);
    if (ms <= 0) return;
    /* The transport is already acquired here. Do not send this from a separate
     * gdbus client: BlueZ restricts Delay writes to the acquiring connection. */
    GError *err = NULL;
    g_dbus_set_property(config.dbus, t->bluez_dbus_owner, t->bluez_dbus_path,
        BLUEZ_IFACE_MEDIA_TRANSPORT, "Delay", g_variant_new_uint16(ms * 10), &err);
    if (err != NULL) {
        warn("OpenAudioHub: rendering-delay report rejected: %s", err->message);
        oah_write_delay_state(t->bluez_dbus_path, ms, "rejected", err->message);
        g_error_free(err);
    }
    else {
        info("OpenAudioHub: requested fixed rendering delay %d ms", ms);
        oah_write_delay_state(t->bluez_dbus_path, ms, "reported", NULL);
    }
}
'''

def patch(text: str) -> str:
    if MARKER in text or 'OpenAudioHub receiver controls v1' in text:
        raise ValueError('Already patched; always start with a clean pinned source export')
    marker='static int a2dp_sbc_sink_transport_start(struct ba_transport *t) {\n'
    if text.count(marker) != 1 or text.count('struct a2dp_sep a2dp_sbc_sink = {') != 1:
        raise ValueError('Unexpected source structure; refusing to patch')
    text=text.replace('#include <stdint.h>','#include <stdint.h>\n#include <stdlib.h>\n#include <stdio.h>\n#include <time.h>')
    text=text.replace('#include "ba-config.h"','#include "ba-config.h"\n#include "bluez-iface.h"\n#include "dbus.h"')
    text=text.replace(marker, INIT+'\n'+marker+'\toah_report_rendering_delay(t);\n')
    pos=text.index('struct a2dp_sep a2dp_sbc_sink = {')
    prefix, sink=text[:pos],text[pos:]
    needle='\t.configuration_select = a2dp_sbc_configuration_select,'
    if sink.count(needle)!=1: raise ValueError('Sink initializer mismatch')
    sink=sink.replace(needle,'\t.init = oah_sbc_sink_init,\n'+needle)
    return prefix+sink

def main():
    ap=argparse.ArgumentParser();ap.add_argument('source',type=Path);args=ap.parse_args()
    source=args.source/'src/a2dp-sbc.c'
    source.write_text(patch(source.read_text()))
    print('Patched SBC sink only; shared maximum and source encoder unchanged.')
if __name__=='__main__': main()
