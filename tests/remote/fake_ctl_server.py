"""Fake Mac bridge for the agr ctl tests.

usage: fake_ctl_server.py DIR

Listens on DIR/s (keep DIR short: a unix socket path holds about 100 bytes) and
serves one connection at a time. For each connection it reads the mode from
DIR/mode and the reply bytes from DIR/reply, and leaves in DIR:
  header  the header line, without its newline
  early   how many bytes arrived before the ack (the body must wait for it)
  body    the bytes received after the header
  conns   the number of connections so far
Modes: normal (ack, body, reply), noack (silent), ackeof (ack, body, close),
refuse (reply without an ack), hang (ack, then wait for the client to go).
"""
import json
import os
import select
import socket
import sys

directory = sys.argv[1]


def path(name):
    return os.path.join(directory, name)


def read_text(name, default=""):
    try:
        with open(path(name)) as handle:
            return handle.read().strip()
    except OSError:
        return default


def write_bytes(name, data):
    with open(path(name), "wb") as handle:
        handle.write(data)


def drain(conn, seconds):
    """Collect what the client sends within the given time."""
    got = b""
    while True:
        ready, _, _ = select.select([conn], [], [], seconds)
        if not ready:
            return got
        chunk = conn.recv(65536)
        if not chunk:
            return got
        got += chunk


def read_exact(conn, buf, size):
    conn.settimeout(3)
    try:
        while len(buf) < size:
            chunk = conn.recv(65536)
            if not chunk:
                break
            buf += chunk
    except OSError:
        pass
    return buf


def serve(conn, count):
    buf = b""
    conn.settimeout(3)
    while b"\n" not in buf:
        chunk = conn.recv(65536)
        if not chunk:
            return
        buf += chunk
    line, _, buf = buf.partition(b"\n")
    write_bytes("header", line)
    header = json.loads(line)
    want = sum(header.get("files", [])) + header.get("stdin", 0)
    mode = read_text("mode") or "normal"
    with open(path("reply"), "rb") as handle:
        reply = handle.read()

    if mode == "noack":
        write_bytes("early", str(len(buf) + len(drain(conn, 6))).encode())
        return
    if mode == "refuse":
        conn.sendall(reply)
        write_bytes("body", buf + drain(conn, 0.5))
        return

    buf += drain(conn, 0.3)
    write_bytes("early", str(len(buf)).encode())
    conn.sendall(b"ack 1\n")
    body = read_exact(conn, buf, want)
    write_bytes("body", body)
    if mode == "hang":
        drain(conn, 30)
        return
    if mode == "normal":
        conn.sendall(reply)


server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
server.bind(path("s"))
server.listen(4)
write_bytes("ready", b"1")
count = 0
while True:
    conn, _ = server.accept()
    count += 1
    write_bytes("conns", str(count).encode())
    try:
        serve(conn, count)
    except (OSError, ValueError):
        pass
    finally:
        conn.close()
