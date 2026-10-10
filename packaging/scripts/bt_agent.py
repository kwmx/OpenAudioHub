#!/usr/bin/env python3
"""OpenAudioHub BlueZ pairing agent.

Runs continuously so headless pairing works without an interactive bluetoothctl.

This agent governs the direction where the *remote* device initiates. A phone or
computer connecting to the hub needs pairing mode enabled (the flag file below);
already paired/trusted devices are always allowed to authorize services and
reconnect, so reconnects never depend on pairing mode.

The opposite direction is deliberately not gated here: adding a headset or speaker
from the web UI discovers the device with Scan and pairs it through the daemon's
own bluetoothctl invocation, so it works with pairing mode off. That is confirmed
behaviour, not an oversight - do not add a pairing-mode check for it.

Re-pairing a device that is already bonded (Windows after "Remove device", or a
phone whose pairing was cleared on one side) reaches this agent because
main.conf sets JustWorksRepairing = confirm. That replaces the stored key, so it
is allowed only while pairing mode is on, never just because the address is known.

A device that finishes pairing from its own side is marked Trusted, so later
reconnects authorize without depending on this agent being up at that moment.
"""
import os
import dbus
import dbus.service
import dbus.mainloop.glib
from gi.repository import GLib

AGENT_PATH = '/org/openaudiohub/agent'
PAIRING_FLAG = '/run/openaudiohub/pairing-enabled'
BLUEZ = 'org.bluez'

def log(msg):
    # stdout goes to the journal; openaudiohubd includes it in Diagnostics.
    print(msg, flush=True)

def addr_of(path):
    return str(path).rsplit('/dev_', 1)[-1].replace('_', ':')

class Rejected(dbus.DBusException):
    _dbus_error_name = 'org.bluez.Error.Rejected'

class Agent(dbus.service.Object):
    def __init__(self, bus):
        super().__init__(bus, AGENT_PATH)
        self.bus = bus

    def pairing_enabled(self):
        return os.path.exists(PAIRING_FLAG)

    def device_flags(self, path):
        try:
            props = dbus.Interface(self.bus.get_object(BLUEZ, path), 'org.freedesktop.DBus.Properties')
            return bool(props.Get('org.bluez.Device1', 'Paired')), bool(props.Get('org.bluez.Device1', 'Trusted'))
        except Exception:
            return False, False

    def device_known(self, path):
        paired, trusted = self.device_flags(path)
        return paired or trusted

    def allow_new(self, device):
        if self.pairing_enabled() or self.device_known(device):
            return
        log('refused %s: unknown device and pairing mode is off' % addr_of(device))
        raise Rejected('OpenAudioHub pairing mode is disabled')

    def allow_pairing(self, device):
        """Pairing requests: like allow_new, but re-pairing a bonded device
        replaces its key and therefore needs pairing mode."""
        if self.pairing_enabled():
            return
        paired, trusted = self.device_flags(device)
        if trusted and not paired:
            # Trusted but never bonded: a pairing the hub itself started.
            return
        log('refused pairing from %s: pairing mode is off' % addr_of(device))
        raise Rejected('OpenAudioHub pairing mode is disabled')

    @dbus.service.method('org.bluez.Agent1', in_signature='', out_signature='')
    def Release(self):
        pass

    @dbus.service.method('org.bluez.Agent1', in_signature='o', out_signature='s')
    def RequestPinCode(self, device):
        self.allow_new(device)
        return '0000'

    @dbus.service.method('org.bluez.Agent1', in_signature='o', out_signature='u')
    def RequestPasskey(self, device):
        self.allow_new(device)
        return dbus.UInt32(0)

    @dbus.service.method('org.bluez.Agent1', in_signature='ou', out_signature='')
    def DisplayPasskey(self, device, passkey):
        pass

    @dbus.service.method('org.bluez.Agent1', in_signature='os', out_signature='')
    def DisplayPinCode(self, device, pincode):
        pass

    @dbus.service.method('org.bluez.Agent1', in_signature='ou', out_signature='')
    def RequestConfirmation(self, device, passkey):
        self.allow_pairing(device)

    @dbus.service.method('org.bluez.Agent1', in_signature='o', out_signature='')
    def RequestAuthorization(self, device):
        self.allow_pairing(device)

    @dbus.service.method('org.bluez.Agent1', in_signature='os', out_signature='')
    def AuthorizeService(self, device, uuid):
        self.allow_new(device)

    @dbus.service.method('org.bluez.Agent1', in_signature='', out_signature='')
    def Cancel(self):
        pass

def trust_when_paired(bus):
    def changed(interface, props, invalidated, path=None):
        if interface != 'org.bluez.Device1' or not path:
            return
        if not (props.get('Paired') or props.get('Bonded')):
            return
        try:
            dev = dbus.Interface(bus.get_object(BLUEZ, path), 'org.freedesktop.DBus.Properties')
            if not bool(dev.Get('org.bluez.Device1', 'Trusted')):
                dev.Set('org.bluez.Device1', 'Trusted', dbus.Boolean(True))
                log('paired %s; marked trusted' % addr_of(path))
        except Exception as e:
            log('could not trust %s: %s' % (addr_of(path), e))
    bus.add_signal_receiver(changed, dbus_interface='org.freedesktop.DBus.Properties',
                            signal_name='PropertiesChanged', bus_name=BLUEZ, path_keyword='path')

def main():
    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SystemBus()
    agent = Agent(bus)
    trust_when_paired(bus)
    manager = dbus.Interface(bus.get_object(BLUEZ, '/org/bluez'), 'org.bluez.AgentManager1')
    try:
        manager.RegisterAgent(AGENT_PATH, 'NoInputNoOutput')
    except dbus.exceptions.DBusException as e:
        if 'AlreadyExists' not in str(e):
            raise
    manager.RequestDefaultAgent(AGENT_PATH)
    log('pairing agent registered')
    GLib.MainLoop().run()

if __name__ == '__main__':
    main()
