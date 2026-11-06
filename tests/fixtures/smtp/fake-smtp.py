#!/usr/bin/env python3
"""Local SMTP server for the installer's SMTP pre-check tests
(tests/install-cloud-helpers.test.sh): 220 greeting, EHLO, STARTTLS or
implicit TLS, AUTH PLAIN/LOGIN against one user and password, NOOP, HELP and
QUIT. It accepts no mail. Every connection and login attempt is appended to
--log ("connect", "auth-ok", "auth-fail"), so a test can count attempts.

Modes: starttls (offers STARTTLS), tls (implicit TLS), none (plain, no
STARTTLS) and silent (accepts the connection, never greets). With
--listen-after the port is bound but not listening for that many seconds:
connections are refused, as with a relay that is restarting. The server
writes its port to --port-file and exits after --lifetime seconds.
"""
import argparse
import base64
import os
import socket
import ssl
import threading
import time

p = argparse.ArgumentParser()
p.add_argument("--port-file", required=True)
p.add_argument("--mode", choices=["starttls", "tls", "none", "silent"], default="starttls")
p.add_argument("--cert")
p.add_argument("--key")
p.add_argument("--user", default="")
p.add_argument("--password", default="")
p.add_argument("--log", default=os.devnull)
p.add_argument("--bind", default="127.0.0.1")
p.add_argument("--listen-after", type=float, default=0)
p.add_argument("--lifetime", type=float, default=60)
a = p.parse_args()

ctx = None
if a.cert:
    ctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
    ctx.load_cert_chain(a.cert, a.key)
log_lock = threading.Lock()


def log(event):
    with log_lock, open(a.log, "a") as f:
        f.write(event + "\n")


def session(conn):
    log("connect")
    if a.mode == "silent":
        time.sleep(a.lifetime)
        return
    secured = a.mode == "tls"
    if secured:
        conn = ctx.wrap_socket(conn, server_side=True)
    f = conn.makefile("rwb")

    def send(line):
        f.write(line.encode() + b"\r\n")
        f.flush()

    def readline():
        return f.readline().decode(errors="replace").strip()

    send("220 fake.test ESMTP")
    while True:
        cmd = readline()
        if not cmd:
            return
        verb = cmd.split(" ")[0].upper()
        if verb in ("EHLO", "HELO"):
            lines = ["fake.test"]
            if a.mode == "starttls" and not secured:
                lines.append("STARTTLS")
            if secured and a.user:
                lines.append("AUTH PLAIN LOGIN")
            lines.append("HELP")
            for i, line in enumerate(lines):
                send(("250 " if i == len(lines) - 1 else "250-") + line)
        elif verb == "STARTTLS" and a.mode == "starttls" and not secured:
            send("220 2.0.0 ready to start TLS")
            conn = ctx.wrap_socket(conn, server_side=True)
            f = conn.makefile("rwb")
            secured = True
        elif verb == "AUTH" and secured and a.user:
            parts = cmd.split(" ")
            if parts[1].upper() == "PLAIN":
                blob = parts[2] if len(parts) > 2 else None
                if blob is None:
                    send("334 ")
                    blob = readline()
                _, user, password = base64.b64decode(blob).decode().split("\0")
            else:
                send("334 VXNlcm5hbWU6")
                user = base64.b64decode(readline()).decode()
                send("334 UGFzc3dvcmQ6")
                password = base64.b64decode(readline()).decode()
            if user == a.user and password == a.password:
                log("auth-ok")
                send("235 2.7.0 Authentication successful")
            else:
                log("auth-fail")
                send("535 5.7.8 Authentication credentials invalid")
        elif verb == "NOOP":
            send("250 2.0.0 OK")
        elif verb == "HELP":
            send("214 2.0.0 fake.test")
        elif verb == "QUIT":
            send("221 2.0.0 bye")
            return
        else:
            send("502 5.5.2 command not implemented")


family = socket.AF_INET6 if ":" in a.bind else socket.AF_INET
srv = socket.socket(family, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind((a.bind, 0))
with open(a.port_file + ".tmp", "w") as pf:
    pf.write(str(srv.getsockname()[1]))
os.rename(a.port_file + ".tmp", a.port_file)
# Bound but not listening: connections are refused until listen().
time.sleep(a.listen_after)
srv.listen(16)
threading.Timer(a.lifetime, lambda: os._exit(0)).start()


def handle(conn):
    try:
        session(conn)
    except (OSError, ssl.SSLError, ValueError, IndexError):
        pass
    finally:
        conn.close()


while True:
    c, _ = srv.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
