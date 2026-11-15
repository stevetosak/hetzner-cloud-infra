#!/usr/bin/env python3
"""Check the mail DNS records of one domain against the Email Plan's record list.

    python3 -I projects/mail/dns_check.py tosak.net
    python3 -I projects/mail/dns_check.py client.example

Every domain gets rows 2 to 8 of the list (MX, the three SES DKIM CNAMEs, the
MAIL FROM MX and SPF, DMARC) and the SES identity's status. The platform domain
`tosak.net` also gets rows 1 and 9 to 14: `mail.tosak.net`, the proxied web
hosts, MTA-STS, TLS reporting and the SRV records.

Each record is read from every authoritative nameserver of the domain, without
recursion, and must match on each. The script prints one line per check and
exits 1 when any check fails. It reads only: `dig`, `aws sesv2
get-email-identity` (an `aws login` session), `kubectl get` of the mail load
balancer's address, and two HTTPS GETs (the MTA-STS policy and Cloudflare's
address ranges). Standard library only.
"""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import re
import subprocess
import sys
import urllib.request
from dataclasses import dataclass
from typing import Callable, Iterable, Protocol

PLATFORM_DOMAIN = "tosak.net"
MAIL_HOST = "mail.tosak.net"
SES_REGION = "eu-central-1"
SES_FEEDBACK_MX = f"feedback-smtp.{SES_REGION}.amazonses.com"
SES_DKIM_SUFFIX = "dkim.amazonses.com"
MAIL_LB_SERVICE = ("mail", "stalwart-mail")
CLOUDFLARE_IPV4_URL = "https://www.cloudflare.com/ips-v4/"
HTTP_TIMEOUT = 10
USER_AGENT = "mail-dns-check"

# A matcher gets the answers of one nameserver and returns a fault, or None.
Matcher = Callable[[list[str]], "str | None"]


class LookupFailed(Exception):
    """A source (DNS, SES, kubectl, HTTPS) could not be read."""


# --- Answers ---------------------------------------------------------------

_TXT_STRING = re.compile(r'"((?:[^"\\]|\\.)*)"')
_TXT_ESCAPE = re.compile(r"\\(\d{3}|.)")


def parse_txt(line: str) -> str:
    """Join the character strings of one TXT answer, as `dig +short` prints it."""
    parts = _TXT_STRING.findall(line)
    if not parts:
        return line

    def unescape(match: re.Match) -> str:
        code = match.group(1)
        return chr(int(code)) if code.isdigit() else code

    return "".join(_TXT_ESCAPE.sub(unescape, part) for part in parts)


def normalize(rtype: str, line: str) -> str:
    """Make one answer comparable: TXT keeps its case, host names lose it and the root dot."""
    if rtype == "TXT":
        return parse_txt(line)
    return " ".join(word.lower().rstrip(".") for word in line.split())


# --- Matchers --------------------------------------------------------------


def exactly(*values: str) -> Matcher:
    want = sorted(values)

    def match(answers: list[str]) -> str | None:
        got = sorted(answers)
        return None if got == want else f"want {want}, got {got}"

    return match


def dmarc_reporting_to(address: str) -> Matcher:
    """One DMARC record, with any policy, whose aggregate reports go to `address`."""

    def match(answers: list[str]) -> str | None:
        if len(answers) != 1:
            return f"want one DMARC record, got {answers}"
        tags = [tag.strip() for tag in answers[0].split(";") if tag.strip()]
        pairs = dict(tag.split("=", 1) for tag in tags if "=" in tag)
        if not tags or tags[0] != "v=DMARC1":
            return f"the record must start with v=DMARC1: {answers[0]!r}"
        if pairs.get("p") not in ("none", "quarantine", "reject"):
            return f"no valid p= tag: {answers[0]!r}"
        reports = [uri.strip() for uri in pairs.get("rua", "").split(",")]
        if f"mailto:{address}" not in reports:
            return f"rua does not name mailto:{address}: {answers[0]!r}"
        return None

    return match


def inside(networks: list[ipaddress.IPv4Network]) -> Matcher:
    """At least one address, and every address in `networks` (a proxied Cloudflare record)."""

    def match(answers: list[str]) -> str | None:
        if not answers:
            return "no A record"
        outside = [a for a in answers if not any(_in(a, net) for net in networks)]
        return f"not a Cloudflare address (DNS only?): {outside}" if outside else None

    return match


def _in(address: str, network: ipaddress.IPv4Network) -> bool:
    try:
        return ipaddress.IPv4Address(address) in network
    except ValueError:
        return False


def unavailable(reason: str) -> Matcher:
    """The expected value could not be learned, so the check fails with the reason."""
    return lambda answers: reason


# --- Sources ---------------------------------------------------------------


class Resolver(Protocol):
    def answers(self, server: str, name: str, rtype: str) -> list[str]: ...


class DigResolver:
    """Asks one nameserver directly, without recursion, through `dig`."""

    def answers(self, server: str, name: str, rtype: str) -> list[str]:
        command = ["dig", "+norecurse", "+short", "+time=3", "+tries=2", f"@{server}", name, rtype]
        result = subprocess.run(command, capture_output=True, text=True)
        if result.returncode != 0:
            detail = (result.stdout + result.stderr).strip().splitlines()
            raise LookupFailed(f"dig {name} {rtype} @{server}: {detail[-1] if detail else result.returncode}")
        return [normalize(rtype, line) for line in result.stdout.splitlines() if line.strip()]


def authoritative_servers(domain: str) -> list[str]:
    result = subprocess.run(["dig", "+short", domain, "NS"], capture_output=True, text=True)
    servers = sorted(line.strip().rstrip(".") for line in result.stdout.splitlines() if line.strip())
    if result.returncode != 0 or not servers:
        raise LookupFailed(f"{domain} has no NS records; give the zone's apex")
    return servers


def read_ses_identity(domain: str) -> dict:
    command = ["aws", "sesv2", "get-email-identity", "--email-identity", domain,
               "--region", SES_REGION, "--output", "json"]
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode != 0:
        detail = result.stderr.strip().splitlines()
        raise LookupFailed(f"aws sesv2 get-email-identity: {detail[-1] if detail else result.returncode}")
    return json.loads(result.stdout)


def read_mail_lb_ipv4() -> list[str]:
    namespace, name = MAIL_LB_SERVICE
    command = ["kubectl", "-n", namespace, "get", "service", name,
               "-o", "jsonpath={.status.loadBalancer.ingress[*].ip}"]
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode != 0:
        raise LookupFailed(f"kubectl get service {namespace}/{name}: {result.stderr.strip()}")
    addresses = [a for a in result.stdout.split() if _is_ipv4(a)]
    if not addresses:
        raise LookupFailed(f"service {namespace}/{name} has no IPv4 load balancer address yet")
    return addresses


def _is_ipv4(text: str) -> bool:
    try:
        ipaddress.IPv4Address(text)
        return True
    except ValueError:
        return False


def _https_get(url: str) -> tuple[bytes, str]:
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT) as response:
            return response.read(), response.headers.get("Content-Type", "")
    except OSError as error:
        raise LookupFailed(f"GET {url}: {error}") from error


def read_cloudflare_networks() -> list[ipaddress.IPv4Network]:
    body, _ = _https_get(CLOUDFLARE_IPV4_URL)
    try:
        networks = [ipaddress.IPv4Network(line.strip()) for line in body.decode().splitlines() if line.strip()]
    except ValueError as error:
        raise LookupFailed(f"{CLOUDFLARE_IPV4_URL}: {error}") from error
    if not networks:
        raise LookupFailed(f"{CLOUDFLARE_IPV4_URL} listed no ranges")
    return networks


def read_mta_sts_policy(domain: str) -> bytes:
    url = f"https://mta-sts.{domain}/.well-known/mta-sts.txt"
    body, content_type = _https_get(url)
    if not content_type.startswith("text/plain"):
        raise LookupFailed(f"{url} is served as {content_type!r}, RFC 8461 wants text/plain")
    return body


# --- SES and MTA-STS rules -------------------------------------------------


def ses_faults(identity: dict, domain: str) -> list[str]:
    dkim = identity.get("DkimAttributes", {})
    mail_from = identity.get("MailFromAttributes", {})
    rules = [
        (identity.get("VerifiedForSendingStatus") is True, "the identity is not verified for sending"),
        (dkim.get("SigningEnabled") is True, "DKIM signing is off"),
        (dkim.get("Status") == "SUCCESS", f"DKIM status is {dkim.get('Status')}"),
        (len(dkim.get("Tokens", [])) == 3, f"want 3 DKIM tokens, got {len(dkim.get('Tokens', []))}"),
        (mail_from.get("MailFromDomain") == f"bounce.{domain}",
         f"MAIL FROM domain is {mail_from.get('MailFromDomain')}, want bounce.{domain}"),
        (mail_from.get("MailFromDomainStatus") == "SUCCESS",
         f"MAIL FROM status is {mail_from.get('MailFromDomainStatus')}"),
        (mail_from.get("BehaviorOnMxFailure") == "USE_DEFAULT_VALUE",
         f"behaviour on MX failure is {mail_from.get('BehaviorOnMxFailure')}, want USE_DEFAULT_VALUE"),
    ]
    return [fault for holds, fault in rules if not holds]


def policy_id(body: bytes) -> str:
    """The `_mta-sts` id: the first 32 hex characters of the policy's sha256 (projects/mail/httproute.yaml)."""
    return hashlib.sha256(body).hexdigest()[:32]


def policy_faults(body: bytes, mail_host: str = MAIL_HOST) -> list[str]:
    """The RFC 8461 fields a sender needs, and an `mx` line that covers `mail_host`."""
    fields: dict[str, list[str]] = {}
    for line in body.decode("utf-8", "replace").splitlines():
        key, sep, value = line.partition(":")
        if sep:
            fields.setdefault(key.strip(), []).append(value.strip())
    faults = []
    if fields.get("version") != ["STSv1"]:
        faults.append(f"version is {fields.get('version')}, want STSv1")
    mode = fields.get("mode", [None])[0]
    if mode not in ("enforce", "testing", "none"):
        faults.append(f"mode is {mode}")
    max_age = fields.get("max_age", [""])[0]
    if not max_age.isdigit() or int(max_age) > 31557600:
        faults.append(f"max_age is {max_age!r}, want 0 to 31557600")
    if mode != "none" and not any(_mx_covers(pattern, mail_host) for pattern in fields.get("mx", [])):
        faults.append(f"no mx line covers {mail_host}: {fields.get('mx')}")
    return faults


def _mx_covers(pattern: str, host: str) -> bool:
    pattern = pattern.lower()
    if pattern.startswith("*."):
        head, _, tail = host.partition(".")
        return bool(head) and tail == pattern[2:]
    return pattern == host


# --- Checks ----------------------------------------------------------------


@dataclass(frozen=True)
class Check:
    row: str
    name: str
    rtype: str
    want: str
    matcher: Matcher


def mail_checks(domain: str, dkim_tokens: list[str] | None) -> list[Check]:
    """Rows 2 to 8: what every mail domain publishes, the platform's own and a client's."""
    checks = [Check("2", domain, "MX", f"10 {MAIL_HOST}", exactly(f"10 {MAIL_HOST}"))]
    if dkim_tokens:
        for index, token in enumerate(sorted(dkim_tokens)):
            target = f"{token}.{SES_DKIM_SUFFIX}"
            checks.append(Check(str(3 + index), f"{token}._domainkey.{domain}", "CNAME", target, exactly(target)))
    else:
        checks.append(Check("3-5", f"<token>._domainkey.{domain}", "CNAME", "three SES DKIM CNAMEs",
                            unavailable("the SES identity was not read, so its DKIM tokens are unknown")))
    checks += [
        Check("6", f"bounce.{domain}", "MX", f"10 {SES_FEEDBACK_MX}", exactly(f"10 {SES_FEEDBACK_MX}")),
        Check("7", f"bounce.{domain}", "TXT", "v=spf1 include:amazonses.com ~all",
              exactly("v=spf1 include:amazonses.com ~all")),
        Check("8", f"_dmarc.{domain}", "TXT", f"v=DMARC1; p=...; rua=mailto:dmarc@{domain}",
              dmarc_reporting_to(f"dmarc@{domain}")),
    ]
    return checks


@dataclass(frozen=True)
class PlatformFacts:
    """What rows 1 and 9 to 14 compare against; a fact that could not be read is a LookupFailed."""

    mail_ipv4: list[str] | LookupFailed
    cloudflare: list[ipaddress.IPv4Network] | LookupFailed
    policy: bytes | LookupFailed


def platform_checks(domain: str, facts: PlatformFacts) -> list[Check]:
    """Rows 1 and 9 to 14: only the platform domain publishes them."""

    def from_fact(fact, build: Callable[..., Matcher]) -> Matcher:
        return unavailable(str(fact)) if isinstance(fact, LookupFailed) else build(fact)

    def mta_sts_matcher(body: bytes) -> Matcher:
        faults = policy_faults(body)
        if faults:
            return unavailable("the served policy is not valid: " + "; ".join(faults))
        return exactly(f"v=STSv1; id={policy_id(body)}")

    proxied = from_fact(facts.cloudflare, inside)
    return [
        Check("1", MAIL_HOST, "A", "the mail load balancer, DNS only",
              from_fact(facts.mail_ipv4, lambda ips: exactly(*ips))),
        Check("9", f"mail-admin.{domain}", "A", "Cloudflare (proxied)", proxied),
        Check("10", f"mta-sts.{domain}", "A", "Cloudflare (proxied)", proxied),
        Check("11", f"_mta-sts.{domain}", "TXT", "v=STSv1; id=<sha256 of the served policy>",
              from_fact(facts.policy, mta_sts_matcher)),
        Check("12", f"_smtp._tls.{domain}", "TXT", f"v=TLSRPTv1; rua=mailto:dmarc@{domain}",
              exactly(f"v=TLSRPTv1; rua=mailto:dmarc@{domain}")),
        Check("13", f"_imaps._tcp.{domain}", "SRV", f"0 1 993 {MAIL_HOST}", exactly(f"0 1 993 {MAIL_HOST}")),
        Check("14", f"_submissions._tcp.{domain}", "SRV", f"0 1 465 {MAIL_HOST}",
              exactly(f"0 1 465 {MAIL_HOST}")),
    ]


@dataclass(frozen=True)
class Result:
    label: str
    want: str
    faults: list[str]

    @property
    def passed(self) -> bool:
        return not self.faults


def evaluate(check: Check, resolver: Resolver, servers: Iterable[str]) -> Result:
    """A check passes only when the answer of every nameserver matches.

    A fault that every nameserver shares is reported once."""
    servers = list(servers)
    servers_by_fault: dict[str, list[str]] = {}
    for server in servers:
        try:
            fault = check.matcher(resolver.answers(server, check.name, check.rtype))
        except LookupFailed as error:
            fault = str(error)
        if fault:
            servers_by_fault.setdefault(fault, []).append(server)
    faults = [
        f"{'every nameserver' if len(servers) > 1 and named == servers else ', '.join(named)}: {fault}"
        for fault, named in servers_by_fault.items()
    ]
    return Result(f"row {check.row:<4} {check.name} {check.rtype}", check.want, faults)


def ses_result(domain: str, identity: dict | LookupFailed) -> Result:
    faults = [str(identity)] if isinstance(identity, LookupFailed) else ses_faults(identity, domain)
    return Result(f"SES      {domain} identity", "verified, DKIM and MAIL FROM successful", faults)


# --- Main ------------------------------------------------------------------


def _attempt(read: Callable[[], object]):
    try:
        return read()
    except LookupFailed as error:
        return error


def gather_platform_facts(domain: str) -> PlatformFacts:
    return PlatformFacts(
        mail_ipv4=_attempt(read_mail_lb_ipv4),
        cloudflare=_attempt(read_cloudflare_networks),
        policy=_attempt(lambda: read_mta_sts_policy(domain)),
    )


def run(domain: str, resolver: Resolver, servers: list[str],
        identity: dict | LookupFailed, facts: PlatformFacts | None) -> list[Result]:
    tokens = None if isinstance(identity, LookupFailed) else identity.get("DkimAttributes", {}).get("Tokens")
    checks = mail_checks(domain, tokens)
    if facts is not None:
        checks += platform_checks(domain, facts)
    checks.sort(key=lambda check: int(check.row.split("-")[0]))
    return [ses_result(domain, identity)] + [evaluate(check, resolver, servers) for check in checks]


def report(results: list[Result], out=sys.stdout) -> bool:
    for result in results:
        if result.passed:
            print(f"PASS  {result.label}", file=out)
            continue
        print(f"FAIL  {result.label}\n        want: {result.want}", file=out)
        for fault in result.faults:
            print(f"        {fault}", file=out)
    passed = sum(result.passed for result in results)
    print(f"\n{passed} of {len(results)} checks pass", file=out)
    return passed == len(results)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("domain", help="the zone's apex, for example tosak.net")
    domain = parser.parse_args(argv).domain.lower().rstrip(".")
    try:
        servers = authoritative_servers(domain)
    except LookupFailed as error:
        print(f"FAIL  {error}", file=sys.stderr)
        return 1
    print(f"Nameservers of {domain}: {', '.join(servers)}\n")
    identity = _attempt(lambda: read_ses_identity(domain))
    facts = gather_platform_facts(domain) if domain == PLATFORM_DOMAIN else None
    results = run(domain, DigResolver(), servers, identity, facts)
    return 0 if report(results) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
