"""Fixed remote helper, sent over authenticated OpenShell SSH (never a login shell).

Only flat, generated names below /sandbox/.ocis-transfer are accessible. This is
scratch space belonging to the sandbox user, NOT an immutable security boundary
against that user. The gateway verifies an export's expected SHA-256 before PUT.
"""

import fcntl
import hashlib
import json
import os
import re
import stat
import sys

MAX_FILE = 32 * 1024 * 1024
MAX_TOTAL = 128 * 1024 * 1024
MAX_ENTRIES = 96
ROOT = "/sandbox/.ocis-transfer"


def directory(path):
    parts = path.strip("/").split("/")
    if not path.startswith("/") or any(part in ("", ".", "..") for part in parts):
        raise ValueError("invalid root")
    # OpenShell permits /sandbox but deliberately denies listing /. Start at the
    # existing allowed top-level directory; retain no-follow checks at every hop.
    # The production path is fixed, not selected by the caller.
    fd = os.open("/" + parts[0], os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[1:]:
            try:
                os.mkdir(part, mode=0o700, dir_fd=fd)
            except FileExistsError:
                pass
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return fd
    except BaseException:
        os.close(fd)
        raise


def regular(fd):
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > MAX_FILE:
        raise ValueError("not a bounded, single-link regular file")
    return info


def capacity(root, additional):
    names = os.listdir(root)
    total = 0
    for name in names:
        info = os.stat(name, dir_fd=root, follow_symlinks=False)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError("unexpected staging entry")
        total += info.st_size
    if len(names) + additional > MAX_ENTRIES or total + MAX_FILE > MAX_TOTAL:
        raise ValueError("staging budget exhausted; release old transfers")


def run(operation, transfer, source, sink, root_path=ROOT):
    if not re.fullmatch(r"[0-9a-f]{32}", transfer):
        raise ValueError("invalid transfer id")
    root = directory(root_path)
    try:
        # Directory flock is shared across all MCP subprocesses for this sandbox.
        fcntl.flock(root, fcntl.LOCK_EX)
        ticket, incoming, outgoing = (
            transfer + suffix for suffix in (".ticket", ".input", ".output")
        )
        if operation in ("reserve", "put"):
            capacity(root, 2 if operation == "put" else 1)
            fd = os.open(
                ticket,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                0o600,
                dir_fd=root,
            )
            os.close(fd)
            created = False
            try:
                if operation == "put":
                    fd = os.open(
                        incoming,
                        os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                        0o600,
                        dir_fd=root,
                    )
                    created = True
                    with os.fdopen(fd, "wb") as target:
                        digest, size = hashlib.sha256(), 0
                        while chunk := source.read(65536):
                            size += len(chunk)
                            if size > MAX_FILE:
                                raise ValueError("transfer too large")
                            target.write(chunk)
                            digest.update(chunk)
                        target.flush()
                        os.fsync(target.fileno())
                    sink.write(
                        json.dumps(
                            {"size": size, "sha256": digest.hexdigest()}
                        ).encode()
                    )
                else:
                    sink.write(b"{}")
            except BaseException:
                if created:
                    os.unlink(incoming, dir_fd=root)
                os.unlink(ticket, dir_fd=root)
                raise
        elif operation == "get":
            ticket_fd = os.open(
                ticket, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=root
            )
            try:
                regular(ticket_fd)
            finally:
                os.close(ticket_fd)
            fd = os.open(
                outgoing, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=root
            )
            with os.fdopen(fd, "rb") as src:
                before = regular(src.fileno())
                count = 0
                while chunk := src.read(65536):
                    count += len(chunk)
                    if count > MAX_FILE:
                        raise ValueError("transfer too large")
                    sink.write(chunk)
                after = regular(src.fileno())
                if (before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (
                    after.st_size,
                    after.st_mtime_ns,
                    after.st_ctime_ns,
                ):
                    raise ValueError("export changed while reading")
        elif operation == "release":
            for name in (incoming, outgoing, ticket):
                try:
                    os.unlink(name, dir_fd=root)
                except FileNotFoundError:
                    pass
            sink.write(b"{}")
        else:
            raise ValueError("unknown operation")
    finally:
        os.close(root)


if __name__ == "__main__":
    try:
        run(sys.argv[1], sys.argv[2], sys.stdin.buffer, sys.stdout.buffer)
    except Exception:
        # Never echo untrusted content or private paths into MCP logs.
        print("staging operation failed", file=sys.stderr)
        sys.exit(1)
