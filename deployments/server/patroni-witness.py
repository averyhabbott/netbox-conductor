#!/usr/bin/env python3
"""
Minimal pysyncobj witness node for NetBox Failover Tool.

Usage:
    patroni-witness.py <self_addr> <partner1_addr> [<partner2_addr> ...]

Example:
    patroni-witness.py 192.168.139.240:5500 192.168.139.76:5433 192.168.139.77:5433

The witness participates in Raft consensus to provide quorum for a 2-node
active/standby cluster without storing any data. It only votes.

Exit codes:
    0 - clean shutdown (SIGTERM)
    1 - startup error (pysyncobj not installed, bad args, etc.)
"""

import signal
import sys
import time


def main():
    if len(sys.argv) < 3:
        print(f"usage: {sys.argv[0]} <self_addr> <partner1> [<partner2> ...]",
              file=sys.stderr)
        sys.exit(1)

    self_addr = sys.argv[1]
    partners = sys.argv[2:]

    try:
        from pysyncobj import SyncObj, SyncObjConf
    except ImportError:
        print("ERROR: pysyncobj is not installed. Run: pip install pysyncobj",
              file=sys.stderr)
        sys.exit(1)

    conf = SyncObjConf(
        autoTick=True,
        connectionRetryTime=5,
        raftMinTimeout=1.5,
        raftMaxTimeout=3.0,
        logLevel='WARNING',
    )

    try:
        obj = SyncObj(self_addr, partners, conf=conf)
    except Exception as e:
        print(f"ERROR: failed to start SyncObj: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"witness started: self={self_addr} partners={partners}", flush=True)

    stop = [False]

    def handle_signal(signum, frame):
        print("witness shutting down...", flush=True)
        stop[0] = True

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    while not stop[0]:
        time.sleep(1)

    obj.destroy()
    print("witness stopped", flush=True)


if __name__ == '__main__':
    main()
