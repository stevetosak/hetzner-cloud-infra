#!/usr/bin/env python3
"""The read-back of the Restore Drill's mail phase (ADR 0003, Amendment 2026-11-02).

Runs as the `check` container of the drill Stalwart pod (drill/mail-pod.yaml),
next to the restored server, which listens on 127.0.0.1 only:

    python3 -I /drill/mail_check.py

1. Lists every user account through JMAP as the drill's recovery admin.
2. Logs in to each over IMAP as `<account>%admin` (impersonation) and counts
   the messages of every mailbox.
3. The canary: one message in CANARY_ACCOUNT must have the sha256 CANARY_SHA256.
4. SAMPLE random messages of all accounts must each come back with a body of
   the size the metadata records; a missing blob fails.

It prints one JSON line (the `mail` field of drill/<timestamp>.json) and exits
0 only on a pass. 🔴 It never prints an address, a mailbox name or a message:
accounts by JMAP id, mailboxes by number, messages by sequence number.

Phase 3 records the canary's sha256 from production with the same fetch:

    python3 -I core/pg-drill/drill/mail_check.py canary-digests \\
        --host mail.tosak.net --port 993 --tls --user drill@tosak.net <password-on-stdin

Standard library only.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import imaplib
import json
import os
import random
import re
import ssl
import sys
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Iterable

ADMIN_USER = "admin"
HTTP_URL = "http://127.0.0.1:8080"
IMAP_HOST = "127.0.0.1"
IMAP_PORT = 143
TIMEOUT = 60

_LIST_LINE = re.compile(rb'^\((?P<flags>[^)]*)\) (?:NIL|"(?:[^"\\]|\\.)*") (?P<name>.+)$')
_SIZE = re.compile(rb"RFC822\.SIZE (\d+)")


class CheckFailed(Exception):
    """The server could not be read at all; the phase fails with this reason."""


# --- IMAP ------------------------------------------------------------------


def mailbox_names(list_response: Iterable) -> list[bytes]:
    """The selectable mailboxes of an IMAP LIST response, as the server names them."""
    names = []
    for item in list_response:
        if item is None:
            continue
        # A name sent as a literal comes as (b'(flags) "/" {n}', b'name').
        head, literal = item if isinstance(item, tuple) else (item, None)
        match = _LIST_LINE.match(head)
        if not match or b"\\noselect" in match.group("flags").lower():
            continue
        names.append(literal if literal is not None else _unquote(match.group("name")))
    return names


def _unquote(name: bytes) -> bytes:
    if name.startswith(b'"') and name.endswith(b'"'):
        return re.sub(rb"\\(.)", rb"\1", name[1:-1])
    return name


def quote(name: bytes) -> str:
    """A mailbox name as an IMAP quoted string (the server sent it in modified UTF-7)."""
    escaped = name.replace(b"\\", b"\\\\").replace(b'"', b'\\"')
    return '"' + escaped.decode("ascii", "replace") + '"'


def message_count(status_response: list) -> int:
    match = re.search(rb"MESSAGES (\d+)", status_response[0] or b"")
    if not match:
        raise CheckFailed("STATUS gave no MESSAGES count")
    return int(match.group(1))


def fetched_message(fetch_response: list) -> tuple[int, bytes] | None:
    """(recorded size, body) from a FETCH of (RFC822.SIZE BODY.PEEK[]), or None."""
    for item in fetch_response:
        if isinstance(item, tuple) and b"BODY[]" in item[0]:
            size = _SIZE.search(item[0])
            if size is None:
                # RFC822.SIZE may come after the literal, in the closing part.
                rest = b" ".join(x for x in fetch_response if isinstance(x, bytes))
                size = _SIZE.search(rest)
            return (int(size.group(1)) if size else -1, item[1] or b"")
    return None


def message_fault(fetch_response: list) -> str | None:
    fetched = fetched_message(fetch_response)
    if fetched is None:
        return "no body returned"
    size, body = fetched
    if not body:
        return "an empty body"
    if size != len(body):
        return f"body of {len(body)} bytes, the metadata records {size}"
    return None


class Mailbox:
    """One IMAP session, logged in as one account."""

    def __init__(self, connect: Callable[[], imaplib.IMAP4], user: str, password: str):
        self.imap = connect()
        status, _ = self.imap.login(user, password)
        if status != "OK":
            raise CheckFailed("IMAP login refused")
        self.selected: bytes | None = None

    def mailboxes(self) -> list[tuple[bytes, int]]:
        status, listing = self.imap.list()
        if status != "OK":
            raise CheckFailed("IMAP LIST failed")
        counts = []
        for name in mailbox_names(listing):
            status, response = self.imap.status(quote(name), "(MESSAGES)")
            if status != "OK":
                raise CheckFailed("IMAP STATUS failed")
            counts.append((name, message_count(response)))
        return counts

    def fetch(self, mailbox: bytes, sequence: int) -> list:
        if self.selected != mailbox:
            status, _ = self.imap.select(quote(mailbox), readonly=True)  # EXAMINE: no \\Seen change
            if status != "OK":
                raise CheckFailed("IMAP EXAMINE failed")
            self.selected = mailbox
        status, response = self.imap.fetch(str(sequence), "(RFC822.SIZE BODY.PEEK[])")
        return response if status == "OK" else []

    def close(self) -> None:
        try:
            self.imap.logout()
        except (imaplib.IMAP4.error, OSError):
            pass


# --- JMAP ------------------------------------------------------------------


def list_accounts(base_url: str, password: str) -> list[tuple[str, str]]:
    """(JMAP id, address) of every user account, through the registry API as the admin."""
    auth = "Basic " + base64.b64encode(f"{ADMIN_USER}:{password}".encode()).decode()

    def call(path: str, body: dict | None = None) -> dict:
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(base_url + path, data=data,
                                         headers={"Authorization": auth, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
                return json.load(response)
        except OSError as error:
            raise CheckFailed(f"JMAP {path}: {error}") from error

    session = call("/jmap/session")
    account = session.get("primaryAccounts", {}).get("urn:stalwart:jmap")
    if not account:
        raise CheckFailed("the JMAP session has no urn:stalwart:jmap account")
    reply = call("/jmap", {
        "using": ["urn:ietf:params:jmap:core", "urn:stalwart:jmap"],
        "methodCalls": [
            ["x:Account/query", {"accountId": account}, "q"],
            ["x:Account/get", {"accountId": account, "properties": ["emailAddress", "@type"],
                               "#ids": {"resultOf": "q", "name": "x:Account/query", "path": "/ids"}}, "g"],
        ],
    })
    responses = {tag: (name, body) for name, body, tag in reply.get("methodResponses", [])}
    name, body = responses.get("g", ("error", {}))
    if name != "x:Account/get":
        raise CheckFailed(f"x:Account/get failed: {name}")
    return [(item["id"], item["emailAddress"]) for item in body.get("list", [])
            if item.get("@type") == "User" and item.get("emailAddress")]


# --- The read-back ---------------------------------------------------------


@dataclass
class Inventory:
    """Every mailbox of every account with its message count, in a fixed order."""

    boxes: list[tuple[str, int, bytes, int]] = field(default_factory=list)  # (account id, box number, name, count)

    @property
    def total(self) -> int:
        return sum(count for *_, count in self.boxes)

    def locate(self, index: int) -> tuple[str, int, bytes, int]:
        """The message at a global index: (account id, box number, box name, sequence number)."""
        for account, number, name, count in self.boxes:
            if index < count:
                return account, number, name, index + 1
            index -= count
        raise IndexError(index)


def sample_indexes(total: int, size: int, rng: random.Random) -> list[int]:
    return sorted(rng.sample(range(total), min(size, total)))


def canary_digests(session: Mailbox) -> list[str]:
    digests = []
    for name, count in session.mailboxes():
        for sequence in range(1, count + 1):
            fetched = fetched_message(session.fetch(name, sequence))
            if fetched and fetched[1]:
                digests.append(hashlib.sha256(fetched[1]).hexdigest())
    return digests


def blob_usage(directory: Path) -> tuple[int, int]:
    files = [path for path in directory.rglob("*") if path.is_file()]
    return len(files), sum(path.stat().st_size for path in files)


@dataclass(frozen=True)
class Settings:
    password: str
    canary_account: str
    canary_sha256: str
    sample: int
    blobs: Path


def read_back(settings: Settings, accounts: list[tuple[str, str]],
              open_session: Callable[[str], Mailbox], rng: random.Random) -> dict:
    """The whole check; every fault lands in `failures`, so one run reports them all."""
    result: dict = {"accounts": len(accounts), "failures": []}
    failures: list[str] = result["failures"]
    if not accounts:
        failures.append("the restored server has no user account")

    sessions: dict[str, Mailbox] = {}
    inventory = Inventory()
    try:
        for account_id, address in accounts:
            sessions[account_id] = session = open_session(address)
            for number, (name, count) in enumerate(session.mailboxes()):
                inventory.boxes.append((account_id, number, name, count))
        result["messages"] = inventory.total

        canary_id = next((aid for aid, address in accounts if address == settings.canary_account), None)
        if not settings.canary_sha256:
            result["canary"] = "not recorded"
            failures.append("no canary sha256 is recorded (CANARY_SHA256)")
        elif canary_id is None:
            result["canary"] = "no account"
            failures.append("the canary account does not exist")
        elif settings.canary_sha256 in canary_digests(sessions[canary_id]):
            result["canary"] = "ok"
        else:
            result["canary"] = "missing"
            failures.append("no message of the canary account has the recorded sha256")

        indexes = sample_indexes(inventory.total, settings.sample, rng)
        result["sampled"] = len(indexes)
        for index in indexes:
            account_id, number, name, sequence = inventory.locate(index)
            fault = message_fault(sessions[account_id].fetch(name, sequence))
            if fault:
                failures.append(f"account {account_id}, mailbox {number}, message {sequence}: {fault}")
    finally:
        for session in sessions.values():
            session.close()

    result["blobFiles"], result["blobBytes"] = blob_usage(settings.blobs)
    result["result"] = "fail" if failures else "pass"
    return result


def settings_from_env() -> Settings:
    return Settings(
        password=Path(os.environ.get("ADMIN_PASSWORD_FILE", "/run/secrets/drill/admin-password")).read_text().strip(),
        canary_account=os.environ.get("CANARY_ACCOUNT", "drill@tosak.net"),
        canary_sha256=os.environ.get("CANARY_SHA256", "").strip().lower(),
        sample=int(os.environ.get("SAMPLE", "20")),
        blobs=Path(os.environ.get("BLOBS_DIR", "/blobs")),
    )


def run_drill() -> int:
    settings = settings_from_env()

    def open_session(address: str) -> Mailbox:
        return Mailbox(lambda: imaplib.IMAP4(IMAP_HOST, IMAP_PORT, timeout=TIMEOUT),
                       f"{address}%{ADMIN_USER}", settings.password)

    try:
        accounts = list_accounts(HTTP_URL, settings.password)
        result = read_back(settings, accounts, open_session, random.SystemRandom())
    except (CheckFailed, imaplib.IMAP4.error, OSError) as error:
        result = {"result": "fail", "failures": [f"the server could not be read: {error}"]}
    print(json.dumps(result, sort_keys=True))
    return 0 if result["result"] == "pass" else 1


def run_canary_digests(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog="mail_check.py canary-digests")
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", type=int, default=993)
    parser.add_argument("--tls", action="store_true", help="implicit TLS, with the certificate verified")
    parser.add_argument("--user", required=True, help="the canary account, or <account>%%admin")
    args = parser.parse_args(argv)
    password = sys.stdin.readline().rstrip("\n")

    def connect() -> imaplib.IMAP4:
        if args.tls:
            return imaplib.IMAP4_SSL(args.host, args.port, ssl_context=ssl.create_default_context(), timeout=TIMEOUT)
        return imaplib.IMAP4(args.host, args.port, timeout=TIMEOUT)

    session = Mailbox(connect, args.user, password)
    try:
        for digest in canary_digests(session):
            print(digest)
    finally:
        session.close()
    return 0


if __name__ == "__main__":
    if sys.argv[1:2] == ["canary-digests"]:
        sys.exit(run_canary_digests(sys.argv[2:]))
    sys.exit(run_drill())
