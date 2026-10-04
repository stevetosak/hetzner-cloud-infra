---
title: "Host keys: seeded at creation, rotated at the first login"
description: "How kluster makes a new server's SSH host key known before the first login, verifies that login against it, and then replaces it, with the checks that prove each step."
type: procedure
verified: 2026-10-04
topics: [provisioning, security]
---

Both build runbooks met the same gap: the first SSH login to a new server had
nothing to check its host key against, and every address the repository
assigns is reused by the next build, so `known_hosts` held a dead machine's key
at each of them ([workers runbook, "Stale host keys"](./workers.md#stale-host-keys-before-the-first-login)).
[ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md) closes it: kluster makes the host key itself, hands it to the server in
cloud-init `user_data`, and pins the public half before the server exists.

The seeded key cannot stay. Hetzner serves `user_data` on the metadata service
at `169.254.169.254` to every process on the server for the server's whole
life, so the private half is readable long after boot. Pods reach that address
unless something blocks it (assumed from the metadata service's design; not
tested on this cluster). **The seeded key is therefore trusted for the first
login only, and replaced during it.**

This page is the runbook the kluster Stage `rotate-host-key` cites
(`tools/kluster/internal/stages/hostkey.go`). A change to its commands changes
this page in the same pull request.

## Seed the key at creation

1. kluster generates an ed25519 key pair per new server
   (`tools/kluster/internal/hostkey`) and renders this cloud-config, which
   deletes the image's host keys, generates none, and installs the pair:

   ```yaml
   #cloud-config
   ssh_deletekeys: true
   ssh_genkeytypes: []
   ssh_keys:
     ed25519_private: |
       -----BEGIN OPENSSH PRIVATE KEY-----
       …
     ed25519_public: ssh-ed25519 AAAA…
   ```

2. The cloud-config goes to Terraform as the sensitive variable `user_data`
   (a map by Worker name in `infra/workers`, a string in
   `infra/control-plane`). It travels in a var file and a saved plan inside a
   run directory under `$XDG_RUNTIME_DIR`, readable by the operator only, which
   kluster deletes when the run ends. The state in R2 holds only a SHA1 of it:
   the hcloud provider's `userDataHashSum` state function, read in the
   released v1.69.0 source (`internal/server/resource.go`).

3. Both Modules carry `lifecycle { ignore_changes = [user_data] }`. The
   attribute is ForceNew, so without it adding the variable to a live server,
   or a new key on the next run, would plan a replacement.

4. After the apply, kluster pins each seeded public key at the server's public
   address in its own `known_hosts`, one file per environment:
   `~/.config/kluster/<env>/known_hosts`. Setting an address replaces every
   earlier pin for it, which is what clears a reused address.

## Verify the first login

kluster logs in as `root` with the pinned file as its only source of trust.
An address with no pin is refused, never learned. While the server boots,
kluster retries a refused or timed-out connection for up to five minutes, but
it never retries a host key mismatch: that means the machine answering is not
the one kluster created, and the run stops there.

## Rotate the seeded key at the first login

Over the connection the seeded key verified, as `root`:

1. Generate the new key on the host and read its public half back:

   ```sh
   set -e
   rm -f /etc/ssh/kluster_new_host_ed25519_key /etc/ssh/kluster_new_host_ed25519_key.pub
   ssh-keygen -q -t ed25519 -N '' -C '' -f /etc/ssh/kluster_new_host_ed25519_key
   cat /etc/ssh/kluster_new_host_ed25519_key.pub
   ```

2. Pin the new key **beside** the seeded one. From here until step 5 either
   key passes, so a run that stops halfway can still reach the host whichever
   key sshd serves.

3. Install it as the only host key, restart sshd, and leave the marker the
   Probe reads:

   ```sh
   set -e
   rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub
   mv /etc/ssh/kluster_new_host_ed25519_key /etc/ssh/ssh_host_ed25519_key
   mv /etc/ssh/kluster_new_host_ed25519_key.pub /etc/ssh/ssh_host_ed25519_key.pub
   chmod 600 /etc/ssh/ssh_host_ed25519_key
   systemctl restart ssh
   date -u +%FT%TZ > /etc/ssh/kluster-host-key-rotated
   ```

4. Dial the host again, accepting the new key and nothing else.

5. Only when that dial succeeds, pin the new key alone. The seeded key is
   then refused everywhere.

## How to know it worked

The Stage's Probe reads `/etc/ssh/kluster-host-key-rotated` over the verified
connection; under `--apply` the Stage must pass its own Probe after acting, or
the run fails. By hand, both of these must hold:

```sh
ssh-keygen -F <address> -f ~/.config/kluster/<env>/known_hosts   # exactly one line
ssh -o UserKnownHostsFile=~/.config/kluster/<env>/known_hosts \
    -o StrictHostKeyChecking=yes root@<address> \
    'ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub; ls /etc/ssh/ssh_host_*_key'
```

The login succeeds without a prompt, and the host lists one key file,
`ssh_host_ed25519_key`, whose fingerprint is the pinned one.
