#!/usr/bin/env python3
"""Patch only the SBC *sink* in the pinned BlueALSA v4.3.1 source.

The shared SBC_MAX_BITPOOL constant and SBC source encoder remain unchanged.
This is not a protocol extension: it selects a lower advertised SBC maximum
and optionally sends a fixed AVDTP rendering-delay report after acquisition.
"""
from pathlib import Path
import argparse

INIT = r'''
/* OpenAudioHub receiver controls v1. These are read only at daemon startup. */
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
        g_error_free(err);
    }
    else
        info("OpenAudioHub: requested fixed rendering delay %d ms", ms);
}
'''

def patch(text: str) -> str:
    if 'OpenAudioHub receiver controls v1' in text:
        raise ValueError('Already patched; always start with a clean pinned source export')
    marker='static int a2dp_sbc_sink_transport_start(struct ba_transport *t) {\n'
    if text.count(marker) != 1 or text.count('struct a2dp_sep a2dp_sbc_sink = {') != 1:
        raise ValueError('Unexpected source structure; refusing to patch')
    text=text.replace('#include <stdint.h>','#include <stdint.h>\n#include <stdlib.h>')
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
