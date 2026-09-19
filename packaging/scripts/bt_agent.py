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
"""
import os
import dbus
import dbus.service
import dbus.mainloop.glib
from gi.repository import GLib

AGENT_PATH = '/org/openaudiohub/agent'
PAIRING_FLAG = '/run/openaudiohub/pairing-enabled'
BLUEZ = 'org.bluez'

class Rejected(dbus.DBusException):
    _dbus_error_name = 'org.bluez.Error.Rejected'

class Agent(dbus.service.Object):
    def __init__(self, bus):
        super().__init__(bus, AGENT_PATH)
        self.bus = bus

    def pairing_enabled(self):
        return os.path.exists(PAIRING_FLAG)

    def device_known(self, path):
        try:
            props = dbus.Interface(self.bus.get_object(BLUEZ, path), 'org.freedesktop.DBus.Properties')
            paired = bool(props.Get('org.bluez.Device1', 'Paired'))
            trusted = bool(props.Get('org.bluez.Device1', 'Trusted'))
            return paired or trusted
        except Exception:
            return False

    def allow_new(self, device):
        if self.pairing_enabled() or self.device_known(device):
            return
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
        self.allow_new(device)

    @dbus.service.method('org.bluez.Agent1', in_signature='o', out_signature='')
    def RequestAuthorization(self, device):
        self.allow_new(device)

    @dbus.service.method('org.bluez.Agent1', in_signature='os', out_signature='')
    def AuthorizeService(self, device, uuid):
        self.allow_new(device)

    @dbus.service.method('org.bluez.Agent1', in_signature='', out_signature='')
    def Cancel(self):
        pass

def main():
    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SystemBus()
    agent = Agent(bus)
    manager = dbus.Interface(bus.get_object(BLUEZ, '/org/bluez'), 'org.bluez.AgentManager1')
    try:
        manager.RegisterAgent(AGENT_PATH, 'NoInputNoOutput')
    except dbus.exceptions.DBusException as e:
        if 'AlreadyExists' not in str(e):
            raise
    manager.RequestDefaultAgent(AGENT_PATH)
    GLib.MainLoop().run()

if __name__ == '__main__':
    main()
