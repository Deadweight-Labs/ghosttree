#!/usr/bin/env python3
"""Run a command on a pseudo terminal, answer its prompts, capture values.

  ptyrun.py --log FILE [--answer REGEX=REPLY]... [--capture REGEX=FILE]... \
            [--timeout SECONDS] -- COMMAND [ARG...]

`ctx join` asks its questions on /dev/tty, so the fresh-VM test needs a real
terminal. Each --answer fires once, when REGEX shows up in the output; each
--capture writes the first group of REGEX to FILE once. The exit status is the
command's (124 on timeout).
"""
import argparse
import os
import pty
import re
import select
import signal
import sys
import time


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--log", required=True)
    ap.add_argument("--answer", action="append", default=[])
    ap.add_argument("--capture", action="append", default=[])
    ap.add_argument("--timeout", type=float, default=300)
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    a = ap.parse_args()
    cmd = a.cmd[1:] if a.cmd and a.cmd[0] == "--" else a.cmd
    if not cmd:
        ap.error("no command")
    answers = [(re.compile(x.rsplit("=", 1)[0]), x.rsplit("=", 1)[1]) for x in a.answer]
    captures = [(re.compile(x.rsplit("=", 1)[0]), x.rsplit("=", 1)[1]) for x in a.capture]
    fired = set()

    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)
    buf = ""
    deadline = time.time() + a.timeout
    status = None
    with open(a.log, "w") as log:
        while True:
            if time.time() > deadline:
                os.kill(pid, signal.SIGTERM)
                status = 124
                break
            r, _, _ = select.select([fd], [], [], 0.5)
            if r:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    data = b""
                if not data:
                    break
                text = data.decode("utf-8", "replace")
                log.write(text.replace("\r", ""))
                log.flush()
                buf += text.replace("\r", "")
                for i, (rx, reply) in enumerate(answers):
                    if ("a", i) not in fired and rx.search(buf):
                        fired.add(("a", i))
                        os.write(fd, (reply + "\n").encode())
                for i, (rx, path) in enumerate(captures):
                    m = rx.search(buf)
                    if ("c", i) not in fired and m:
                        fired.add(("c", i))
                        with open(path, "w") as f:
                            f.write(m.group(1))
    _, st = os.waitpid(pid, 0)
    if status is None:
        status = os.waitstatus_to_exitcode(st)
    sys.exit(status)


main()
