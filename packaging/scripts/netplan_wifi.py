#!/usr/bin/env python3
import argparse, json, os, pathlib, stat
import yaml

NETPLAN = pathlib.Path('/etc/netplan')
MANAGED = NETPLAN / '90-openaudiohub.yaml'

def load_yaml(path):
    try:
        with path.open() as f:
            return yaml.safe_load(f) or {}
    except FileNotFoundError:
        return {}

def save_yaml(path, data, mode=0o600):
    tmp = path.with_suffix(path.suffix + '.tmp')
    with tmp.open('w') as f:
        yaml.safe_dump(data, f, default_flow_style=False, sort_keys=False, allow_unicode=True)
    os.chmod(tmp, mode)
    os.replace(tmp, path)

def strip_interface(interface):
    for path in sorted(list(NETPLAN.glob('*.yaml')) + list(NETPLAN.glob('*.yml'))):
        if path == MANAGED:
            continue
        data = load_yaml(path)
        net = data.get('network') if isinstance(data, dict) else None
        if not isinstance(net, dict):
            continue
        wifis = net.get('wifis')
        if not isinstance(wifis, dict) or interface not in wifis:
            continue
        del wifis[interface]
        if not wifis:
            net.pop('wifis', None)
        save_yaml(path, data, stat.S_IMODE(path.stat().st_mode) or 0o600)

def cmd_set(payload_path):
    payload = json.load(open(payload_path))
    interface = payload['interface']
    strip_interface(interface)
    ap = {}
    if payload.get('password'):
        ap['password'] = payload['password']
    if payload.get('bssid'):
        ap['bssid'] = payload['bssid']
    if payload.get('band') in ('5GHz', '2.4GHz'):
        ap['band'] = payload['band']
    data = {
        'network': {
            'version': 2,
            'renderer': 'networkd',
            'wifis': {
                interface: {
                    'dhcp4': True,
                    'optional': True,
                    'access-points': {payload['ssid']: ap},
                }
            }
        }
    }
    save_yaml(MANAGED, data, 0o600)

if __name__ == '__main__':
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='cmd', required=True)
    s = sub.add_parser('set')
    s.add_argument('--payload', required=True)
    args = p.parse_args()
    if args.cmd == 'set':
        cmd_set(args.payload)
