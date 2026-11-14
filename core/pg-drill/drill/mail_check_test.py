"""Offline tests of mail_check.py: python3 -I -m unittest discover -s core/pg-drill/drill -p '*_test.py'"""

import hashlib
import random
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mail_check as mc  # noqa: E402


def fetch_reply(body: bytes, size: int | None = None) -> list:
    size = len(body) if size is None else size
    return [(b"1 (RFC822.SIZE %d BODY[] {%d}" % (size, len(body)), body), b")"]


class FakeSession:
    """An account's mailboxes as {name: [message bytes or None for a missing blob]}."""

    def __init__(self, boxes: dict[bytes, list]):
        self.boxes = boxes
        self.closed = False

    def mailboxes(self):
        return [(name, len(messages)) for name, messages in self.boxes.items()]

    def fetch(self, mailbox, sequence):
        body = self.boxes[mailbox][sequence - 1]
        return [b"1 (RFC822.SIZE 99)"] if body is None else fetch_reply(body)

    def close(self):
        self.closed = True


CANARY = b"Subject: canary\r\n\r\nthe drill canary\r\n"
ACCOUNTS = [("a", "drill@tosak.net"), ("b", "user@tosak.net")]


def settings(blobs: Path, canary_sha256: str = hashlib.sha256(CANARY).hexdigest(), sample: int = 20):
    return mc.Settings(password="x", canary_account="drill@tosak.net", canary_sha256=canary_sha256,
                       sample=sample, blobs=blobs)


class Parsing(unittest.TestCase):
    def test_list_keeps_selectable_mailboxes_and_unquotes_them(self):
        listing = [b'(\\HasNoChildren) "/" "INBOX"', b'(\\Noselect \\HasChildren) "/" "Archive"',
                   b'(\\Drafts) "/" "Deleted \\"Items\\""', b'() NIL Plain',
                   (b'() "/" {6}', b'Caf&AOk-'), None]
        self.assertEqual(mc.mailbox_names(listing), [b"INBOX", b'Deleted "Items"', b"Plain", b"Caf&AOk-"])

    def test_quote_escapes_for_the_imap_command(self):
        self.assertEqual(mc.quote(b'a "b" \\c'), '"a \\"b\\" \\\\c"')

    def test_status_count(self):
        self.assertEqual(mc.message_count([b'"INBOX" (MESSAGES 12)']), 12)
        with self.assertRaises(mc.CheckFailed):
            mc.message_count([b'"INBOX" ()'])

    def test_a_whole_body_of_the_recorded_size_passes(self):
        self.assertIsNone(mc.message_fault(fetch_reply(b"hello")))

    def test_size_after_the_literal_is_read(self):
        reply = [(b"1 (BODY[] {5}", b"hello"), b" RFC822.SIZE 5)"]
        self.assertIsNone(mc.message_fault(reply))

    def test_a_missing_short_or_empty_body_fails(self):
        self.assertEqual(mc.message_fault([b"1 (RFC822.SIZE 5)"]), "no body returned")
        self.assertEqual(mc.message_fault([]), "no body returned")
        self.assertIn("metadata records 9", mc.message_fault(fetch_reply(b"hello", size=9)))
        self.assertEqual(mc.message_fault(fetch_reply(b"", size=0)), "an empty body")


class Sampling(unittest.TestCase):
    def test_locate_walks_the_boxes_in_order(self):
        inventory = mc.Inventory([("a", 0, b"INBOX", 2), ("a", 1, b"Sent", 0), ("b", 0, b"INBOX", 3)])
        self.assertEqual(inventory.total, 5)
        self.assertEqual(inventory.locate(1), ("a", 0, b"INBOX", 2))
        self.assertEqual(inventory.locate(2), ("b", 0, b"INBOX", 1))
        with self.assertRaises(IndexError):
            inventory.locate(5)

    def test_sample_is_unique_and_at_most_the_total(self):
        self.assertEqual(mc.sample_indexes(3, 20, random.Random(1)), [0, 1, 2])
        picked = mc.sample_indexes(1000, 20, random.Random(1))
        self.assertEqual(len(set(picked)), 20)


class ReadBack(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.blobs = Path(self.tmp.name)
        (self.blobs / "one").write_bytes(b"12345")
        self.sessions = {
            "drill@tosak.net": FakeSession({b"INBOX": [CANARY]}),
            "user@tosak.net": FakeSession({b"INBOX": [b"m1", b"m2"], b"Sent": [b"m3"]}),
        }

    def tearDown(self):
        self.tmp.cleanup()

    def run_check(self, accounts=ACCOUNTS, **overrides):
        return mc.read_back(settings(self.blobs, **overrides), accounts,
                            lambda address: self.sessions[address], random.Random(7))

    def test_a_complete_restore_passes(self):
        result = self.run_check()
        self.assertEqual(result["result"], "pass", result)
        self.assertEqual((result["accounts"], result["messages"], result["sampled"]), (2, 4, 4))
        self.assertEqual((result["canary"], result["blobFiles"], result["blobBytes"]), ("ok", 1, 5))
        self.assertTrue(all(session.closed for session in self.sessions.values()))

    def test_a_missing_blob_fails_and_names_no_address(self):
        self.sessions["user@tosak.net"].boxes[b"Sent"] = [None]
        result = self.run_check()
        self.assertEqual(result["failures"], ["account b, mailbox 1, message 1: no body returned"])
        self.assertNotIn("tosak.net", str(result))
        self.assertNotIn("Sent", str(result))

    def test_the_sample_is_capped(self):
        self.assertEqual(self.run_check(sample=2)["sampled"], 2)

    def test_a_changed_canary_fails(self):
        self.sessions["drill@tosak.net"].boxes[b"INBOX"] = [CANARY + b"x"]
        result = self.run_check()
        self.assertEqual((result["result"], result["canary"]), ("fail", "missing"))

    def test_no_recorded_canary_fails(self):
        result = self.run_check(canary_sha256="")
        self.assertEqual((result["result"], result["canary"]), ("fail", "not recorded"))

    def test_no_canary_account_fails(self):
        result = self.run_check(accounts=[ACCOUNTS[1]])
        self.assertEqual((result["result"], result["canary"]), ("fail", "no account"))

    def test_no_account_at_all_fails(self):
        result = self.run_check(accounts=[])
        self.assertEqual(result["result"], "fail")
        self.assertIn("the restored server has no user account", result["failures"])


if __name__ == "__main__":
    unittest.main()
