#!/usr/bin/env python3
"""Minimal TCP forwarder for the fresh-VM test: tcpfwd.py LHOST LPORT RHOST RPORT.

The VM must reach the host's loopback-only test server under the SAME address
(http://127.0.0.1:PORT), because `ctx join` and the shown install command only
accept https or loopback. Incus cannot proxy loopback into a VM (NAT mode only),
so one forwarder runs on the host bridge address and one inside the VM.
"""
import socket
import sys
import threading


def pipe(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def main():
    lhost, lport, rhost, rport = sys.argv[1], int(sys.argv[2]), sys.argv[3], int(sys.argv[4])
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((lhost, lport))
    srv.listen(64)
    while True:
        c, _ = srv.accept()
        try:
            r = socket.create_connection((rhost, rport), timeout=10)
        except OSError:
            c.close()
            continue
        r.settimeout(None)
        threading.Thread(target=pipe, args=(c, r), daemon=True).start()
        threading.Thread(target=pipe, args=(r, c), daemon=True).start()


main()
