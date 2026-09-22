# Secrets — SOPS + age

How every Secret in this cluster is stored, applied, recovered and rotated.
The decision is ADR 0003 and its 2026-09-22 amendment. The tool is
`scripts/secrets.sh`; this file is what that script cannot tell you.

🔴 **This repository is public.** Every `*.enc.yaml` is published for ever.
The Secrets are safe exactly as long as the age private keys are safe.

---

## 1. The keys

Two age keys can decrypt every file. `.sops.yaml` lists both public halves, and
**either private half alone** decrypts.

| Key | Where the private half lives | Used for |
|---|---|---|
| **primary** | `~/.config/sops/age/keys.txt` (mode 600, owned by the operator) and the password manager | every day |
| **backup** | offline only — never on a machine that runs this repo | recovery, and the drill in section 4 |

The backup exists because one key is a single point of failure: lose it, and
every Secret in git is noise. The backup is useless if it is ever stored next
to the primary.

## 2. One-time setup on a new machine

```bash
# sops, pinned — scripts/secrets.sh warns on any other version
#   https://github.com/getsops/sops/releases/tag/v3.13.3
sops --version

# the primary private key, from the password manager
mkdir -p ~/.config/sops/age
$EDITOR ~/.config/sops/age/keys.txt
chmod 600 ~/.config/sops/age/keys.txt
age-keygen -y ~/.config/sops/age/keys.txt    # must print a recipient in .sops.yaml

# refuse commits of unencrypted *.enc.yaml
git config core.hooksPath .githooks

# keep the editor from writing plaintext swap and undo files
export SOPS_EDITOR="vim -n -i NONE --cmd 'set noundofile nobackup nowritebackup'"
```

Put the `SOPS_EDITOR` line in the shell profile. `sops edit` writes the
plaintext to a `0600` directory under `/tmp` and deletes it afterwards, but
`/tmp` on the operator's laptop is on disk, and vim writes its own files
unless told not to.

Check the key file's **owner** as well as its mode. A file made with `sudo`
is `root`-owned and unreadable, and the error that follows does not say so.

## 3. Everyday use

| Task | Command |
|---|---|
| New Secret, value already in a file or the cluster | `kubectl create secret generic … --dry-run=client -o yaml \| scripts/secrets.sh encrypt <path>.enc.yaml` |
| New Secret, typed value (zsh) | `read -rs 'P?VALUE: '; echo` then the line above with `--from-literal=KEY="$P"`, then `unset P` |
| Change a value | `scripts/secrets.sh edit <path>.enc.yaml` |
| Does the cluster match git? | `scripts/secrets.sh diff projects/<project>` — values print as `***` |
| Apply | `scripts/secrets.sh apply projects/<project>` |

Prefer `encrypt` from a pipe over `edit`: the plaintext never reaches disk.

**Check a value without reading it** — compare its length, or `cmp` it
against its source:

```bash
sops decrypt --extract '["data"]["DB_PASS"]' <file> | base64 -d | wc -c
```

**Copies must move together.** `authos` `credentials` holds copies of
`pg-cluster/db-credentials` and `redis/redis`. Rotate a source and every copy
in the same commit and the same apply.

## 4. Backup drill — every three months, and after any key change

Proves the backup key still decrypts. A backup nobody has tested is not a
backup.

```bash
# on a machine with the backup key, NOT the primary
SOPS_AGE_KEY_FILE=<path to the backup key> \
  sops decrypt --extract '["data"]["keystore.p12"]' \
  projects/authos/api/manifests/keystore.enc.yaml | base64 -d | wc -c
```

Pass: a non-zero byte count. Remove the key file from that machine afterwards.

## 5. Adding or removing a recipient

```bash
$EDITOR .sops.yaml                    # add or remove a public key under `age:`
sops updatekeys -y <every *.enc.yaml> # re-wraps the data key; values are unchanged
scripts/secrets.sh check
```

`updatekeys` needs a private key that can already decrypt. Removing a recipient
this way does **not** protect old commits — see section 6.

## 6. 🔴 If a private key leaks

Assume every value ever committed is readable: the old ciphertext stays in
the public history, and no later commit changes that. New keys alone fix
nothing; **the values themselves must change.**

1. Make a new age key. Replace the leaked recipient in `.sops.yaml`.
2. `sops updatekeys -y` then `sops rotate -i` on every `*.enc.yaml` — new data
   keys, readable only by the new recipients.
3. **Change every real value**, source first, then every copy:
   - database password (`pg-cluster/db-credentials`, then `authos/credentials`)
   - Redis password (`redis/redis`, then `authos/credentials`)
   - `duster-admin` token (and `dstr`)
   - **the authos keystore.** A new signing key means every token Authos has
     issued stops verifying, and every user signs in again. A new
     `authos-credentials-encrypt` key means anything encrypted with the old one
     is unreadable — plan that migration before you rotate it.
4. `scripts/secrets.sh apply` each project, restart the consumers, commit.

## 7. What is deliberately NOT done

- **No `mac_only_encrypted`.** The default MAC covers the unencrypted fields
  too, so a changed `name` or `namespace` fails decryption instead of applying
  a Secret somewhere else.
- **No CI check.** CI runs after the push, and after the push a plaintext
  file is already public. The pre-commit hook is the guard, and it works only
  where `core.hooksPath` is set.
- **No post-quantum recipient yet.** SOPS parses hybrid `age1pq1…` recipients,
  but age calls them experimental. Revisit when that changes: the
  ciphertext here is public for ever, which is exactly the "collect now,
  decrypt later" case they exist for.
- **No ArgoCD plugin.** Secrets are applied out of band, like every other
  object that holds a value set once (ADR 0003).
