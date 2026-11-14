"""Offline tests of dns_check.py: python3 -I -m unittest discover -s projects/mail -p '*_test.py'"""

import ipaddress
import io
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import dns_check as dc  # noqa: E402

# The policy in projects/mail/httproute.yaml and the id its comment gives.
POLICY = b"version: STSv1\nmode: testing\nmx: mail.tosak.net\nmax_age: 86400\n"
POLICY_ID = "4905e08b6a657d5ef7e28f323fee1653"

TOKENS = ["tokb", "toka", "tokc"]
MAIL_IP = "203.0.113.25"
CLOUDFLARE = [ipaddress.IPv4Network("104.16.0.0/13"), ipaddress.IPv4Network("172.64.0.0/13")]
SERVERS = ["ns-a", "ns-b"]


def identity(**overrides):
    value = {
        "VerifiedForSendingStatus": True,
        "DkimAttributes": {"SigningEnabled": True, "Status": "SUCCESS", "Tokens": list(TOKENS)},
        "MailFromAttributes": {"MailFromDomain": "bounce.tosak.net", "MailFromDomainStatus": "SUCCESS",
                               "BehaviorOnMxFailure": "USE_DEFAULT_VALUE"},
    }
    value.update(overrides)
    return value


def zone(domain="tosak.net"):
    """Every record of the list, as a correct zone answers it."""
    records = {
        (domain, "MX"): ["10 mail.tosak.net"],
        (f"bounce.{domain}", "MX"): ["10 feedback-smtp.eu-central-1.amazonses.com"],
        (f"bounce.{domain}", "TXT"): ["v=spf1 include:amazonses.com ~all"],
        (f"_dmarc.{domain}", "TXT"): [f"v=DMARC1; p=none; rua=mailto:dmarc@{domain}"],
        ("mail.tosak.net", "A"): [MAIL_IP],
        (f"mail-admin.{domain}", "A"): ["104.21.84.103", "172.67.191.16"],
        (f"mta-sts.{domain}", "A"): ["104.21.84.103", "172.67.191.16"],
        (f"_mta-sts.{domain}", "TXT"): [f"v=STSv1; id={POLICY_ID}"],
        (f"_smtp._tls.{domain}", "TXT"): [f"v=TLSRPTv1; rua=mailto:dmarc@{domain}"],
        (f"_imaps._tcp.{domain}", "SRV"): ["0 1 993 mail.tosak.net"],
        (f"_submissions._tcp.{domain}", "SRV"): ["0 1 465 mail.tosak.net"],
    }
    for token in TOKENS:
        records[(f"{token}._domainkey.{domain}", "CNAME")] = [f"{token}.dkim.amazonses.com"]
    return records


class FakeResolver:
    def __init__(self, records, per_server=None, failing=()):
        self.records = records
        self.per_server = per_server or {}
        self.failing = set(failing)

    def answers(self, server, name, rtype):
        if server in self.failing:
            raise dc.LookupFailed(f"dig {name} {rtype} @{server}: connection timed out")
        return self.per_server.get((server, name, rtype), self.records.get((name, rtype), []))


def facts(**overrides):
    value = {"mail_ipv4": [MAIL_IP], "cloudflare": CLOUDFLARE, "policy": POLICY}
    value.update(overrides)
    return dc.PlatformFacts(**value)


def key(result):
    """The row number of a DNS check, or SES."""
    first, second = result.label.split()[:2]
    return second if first == "row" else first


def failed(results):
    return {key(r) for r in results if not r.passed}


class Answers(unittest.TestCase):
    def test_txt_strings_are_joined_and_unescaped(self):
        self.assertEqual(dc.parse_txt('"v=spf1 include:" "amazonses.com ~all"'), "v=spf1 include:amazonses.com ~all")
        self.assertEqual(dc.parse_txt(r'"a\"b\\c\059d"'), 'a"b\\c;d')

    def test_host_names_lose_case_and_the_root_dot(self):
        self.assertEqual(dc.normalize("MX", "10 Mail.Tosak.NET."), "10 mail.tosak.net")
        self.assertEqual(dc.normalize("TXT", '"v=DMARC1; P=none"'), "v=DMARC1; P=none")


class Matchers(unittest.TestCase):
    def test_exactly_wants_the_whole_set(self):
        match = dc.exactly("10 mail.tosak.net")
        self.assertIsNone(match(["10 mail.tosak.net"]))
        self.assertIsNotNone(match([]))
        self.assertIsNotNone(match(["10 mail.tosak.net", "20 other.example"]))

    def test_dmarc_needs_one_record_with_the_report_address(self):
        match = dc.dmarc_reporting_to("dmarc@tosak.net")
        self.assertIsNone(match(["v=DMARC1; p=quarantine; rua=mailto:x@y.example,mailto:dmarc@tosak.net"]))
        self.assertIn("rua", match(["v=DMARC1; p=none"]))
        self.assertIn("one DMARC", match(["v=DMARC1; p=none; rua=mailto:dmarc@tosak.net"] * 2))
        self.assertIn("v=DMARC1", match(["p=none; v=DMARC1; rua=mailto:dmarc@tosak.net"]))
        self.assertIn("p=", match(["v=DMARC1; rua=mailto:dmarc@tosak.net"]))

    def test_a_proxied_record_holds_only_cloudflare_addresses(self):
        match = dc.inside(CLOUDFLARE)
        self.assertIsNone(match(["104.21.84.103", "172.67.191.16"]))
        self.assertIn("no A record", match([]))
        self.assertIn("DNS only", match(["104.21.84.103", MAIL_IP]))


class SesAndPolicy(unittest.TestCase):
    def test_a_good_identity_has_no_fault(self):
        self.assertEqual(dc.ses_faults(identity(), "tosak.net"), [])

    def test_each_identity_fault_is_named(self):
        bad = identity(VerifiedForSendingStatus=False,
                       MailFromAttributes={"MailFromDomain": "tosak.net", "MailFromDomainStatus": "PENDING",
                                           "BehaviorOnMxFailure": "REJECT_MESSAGE"})
        faults = dc.ses_faults(bad, "tosak.net")
        self.assertEqual(len(faults), 4, faults)

    def test_the_id_matches_the_shell_command_in_httproute_yaml(self):
        self.assertEqual(dc.policy_id(POLICY), POLICY_ID)
        source = (Path(__file__).resolve().parent / "httproute.yaml").read_text()
        self.assertIn(POLICY_ID, source)
        self.assertIn("\n".join("    " + line for line in POLICY.decode().splitlines()), source)

    def test_a_valid_policy_has_no_fault(self):
        self.assertEqual(dc.policy_faults(POLICY), [])
        self.assertEqual(dc.policy_faults(POLICY.replace(b"\n", b"\r\n")), [])
        self.assertEqual(dc.policy_faults(POLICY.replace(b"mx: mail", b"mx: *")), [])

    def test_a_policy_must_cover_the_mail_host(self):
        self.assertTrue(dc.policy_faults(POLICY.replace(b"mail.tosak.net", b"mx.tosak.net")))
        self.assertTrue(dc.policy_faults(POLICY.replace(b"mx: mail.tosak.net", b"mx: *.mail.tosak.net")))
        self.assertTrue(dc.policy_faults(POLICY.replace(b"testing", b"strict")))
        self.assertTrue(dc.policy_faults(POLICY.replace(b"86400", b"31557601")))


class Run(unittest.TestCase):
    def test_a_correct_platform_zone_passes_every_row(self):
        results = dc.run("tosak.net", FakeResolver(zone()), SERVERS, identity(), facts())
        self.assertEqual(failed(results), set())
        self.assertEqual([key(r) for r in results], ["SES"] + [str(row) for row in range(1, 15)])
        self.assertTrue(dc.report(results, out=io.StringIO()))

    def test_a_client_domain_gets_rows_2_to_8_only(self):
        client = identity(MailFromAttributes={"MailFromDomain": "bounce.client.example",
                                              "MailFromDomainStatus": "SUCCESS",
                                              "BehaviorOnMxFailure": "USE_DEFAULT_VALUE"})
        results = dc.run("client.example", FakeResolver(zone("client.example")), SERVERS, client, None)
        self.assertEqual(failed(results), set())
        rows = [key(r) for r in results[1:]]
        self.assertEqual(rows, ["2", "3", "4", "5", "6", "7", "8"])

    def test_one_nameserver_that_differs_fails_the_row_and_is_named(self):
        resolver = FakeResolver(zone(), per_server={("ns-b", "tosak.net", "MX"): ["10 old.example"]})
        results = dc.run("tosak.net", resolver, SERVERS, identity(), facts())
        self.assertEqual(failed(results), {"2"})
        row = next(r for r in results if key(r) == "2")
        self.assertEqual(len(row.faults), 1)
        self.assertTrue(row.faults[0].startswith("ns-b:"))

    def test_a_nameserver_that_does_not_answer_fails_every_row(self):
        results = dc.run("tosak.net", FakeResolver(zone(), failing={"ns-a"}), SERVERS, identity(), facts())
        self.assertEqual(len(failed(results)), 14)

    def test_an_id_that_was_not_changed_with_the_policy_fails(self):
        changed = POLICY.replace(b"testing", b"enforce")
        results = dc.run("tosak.net", FakeResolver(zone()), SERVERS, identity(), facts(policy=changed))
        self.assertEqual(failed(results), {"11"})

    def test_a_dns_only_web_host_fails(self):
        records = zone()
        records[("mta-sts.tosak.net", "A")] = ["198.51.100.7"]
        self.assertEqual(failed(dc.run("tosak.net", FakeResolver(records), SERVERS, identity(), facts())), {"10"})

    def test_an_unread_source_fails_its_rows_with_the_reason(self):
        missing = dc.LookupFailed("aws sesv2 get-email-identity: token expired")
        results = dc.run("tosak.net", FakeResolver(zone()), SERVERS, missing, facts(
            mail_ipv4=dc.LookupFailed("no IPv4 load balancer address yet")))
        self.assertEqual(failed(results), {"SES", "3-5", "1"})
        self.assertIn("token expired", results[0].faults[0])

    def test_extra_or_missing_records_fail(self):
        records = zone()
        records[("bounce.tosak.net", "TXT")] = ["v=spf1 include:amazonses.com ~all", "v=spf1 -all"]
        del records[("_imaps._tcp.tosak.net", "SRV")]
        records[("_dmarc.tosak.net", "TXT")] = ["v=DMARC1; p=none"]
        results = dc.run("tosak.net", FakeResolver(records), SERVERS, identity(), facts())
        self.assertEqual(failed(results), {"7", "8", "13"})
        missing = next(r for r in results if key(r) == "13")
        self.assertEqual(missing.faults, ["every nameserver: want ['0 1 993 mail.tosak.net'], got []"])


if __name__ == "__main__":
    unittest.main()
