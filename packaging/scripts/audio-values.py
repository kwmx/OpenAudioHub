#!/usr/bin/env python3
"""Validate and print non-secret runtime options. No shell evaluation."""
import json, sys
from pathlib import Path
p=Path(sys.argv[1]) if len(sys.argv)>1 else Path('/etc/openaudiohub/audio.json')
try:
    a=json.loads(p.read_text())
    if not isinstance(a,dict): raise ValueError('audio configuration must be an object')
    def num(k,d):
        n=a.get(k,d)
        if type(n) is not int: raise ValueError(k+' must be an integer')
        return n
    period=num('bluealsaPeriodUs',100000);buffer=num('bluealsaBufferUs',500000)
    cap=num('secondarySbcMaxBitpool',35);delay=num('secondaryAdvertisedDelayMs',0)
    if not 10000<=period<=250000 or period%10000: raise ValueError('invalid period')
    if not period*3<=buffer<=1000000 or buffer%period: raise ValueError('invalid buffer')
    if cap not in (35,53,64,250): raise ValueError('invalid SBC maximum')
    if not 0<=delay<=2000: raise ValueError('invalid advertised delay')
    print(period);print(buffer);print(cap);print(delay)
except (OSError,ValueError,TypeError) as e:
    print(f'OpenAudioHub audio configuration error: {e}',file=sys.stderr)
    sys.exit(78)
