---
title: "Secrets — kluster, SOPS and age"
description: "How every Secret in the cluster is catalogued, made, rotated and applied with kluster secrets, how Reloader restarts the readers, and the backup-key drill and the leaked-key steps."
type: procedure
topics: [secrets, security]
---

How every Secret in this cluster is stored, made, rotated and applied. The
decision is [ADR 0003](../adr/0003-recoverability.md) and its 2026-09-22 and
2026-10-06 amendments; the build is recorded in
[the Log of 2026-10-20](../logs/2026-10-20-kluster-secrets-and-reloader-built-every-secret-rotated-live.md).
The tool is `kluster secrets` (`cd tools/kluster && go run . secrets --help`);
this page is what its help text cannot tell you.

🔴 **This repository is public.** Every `*.enc.yaml` is published for ever.
The Secrets are safe exactly as long as the age private keys are safe.

---

## 1. The catalog and the keys

[`secrets.yaml`](../../secrets.yaml) at the repository root is the Secret
Catalog ([`CONTEXT.md`](../../CONTEXT.md)). Each entry is `<namespace>/<name>`,
names its `*.enc.yaml` file, and gives each key a kind and a note. It holds no
value. The notes are the place for what a Secret must contain and why; the
blank `credentials.yaml` templates no longer exist.

| Kind | Where the value comes from |
|---|---|
| `generated` | random and alphanumeric, made by kluster (40 characters unless `length` says otherwise) |
| `issued` | a provider, kept in the Bitwarden item and field (or attachment) the entry names |
| `copy` | another key's value, `from: <namespace>/<name>.<key>`, with an optional `format` holding `{value}` |
| `literal` | a value that is not secret but must be inside a Secret (today only `db-credentials.username`) |

Two age keys can decrypt every file. `.sops.yaml` lists both public halves, and
**either private half alone** decrypts.

| Key | Where the private half lives | Used for |
|---|---|---|
| **primary** | the Bitwarden item `kluster/age-key`; kluster reads it and hands it to `sops` as `SOPS_AGE_KEY` in the child process only | every day |
| **backup** | offline only, never on a machine that runs this repo | recovery, and the drill in section 5 |

The backup exists because one key is a single point of failure: lose it, and
every Secret in git is noise. The backup is useless if it is ever stored next
to the primary. The amendment decided that `~/.config/sops/age/keys.txt` leaves
the laptop disk; whether a given machine still has it was not checked.

## 2. One-time setup on a new machine

kluster reads the vault through `rbw`, an unofficial Bitwarden client whose
agent keeps the unlocked keys, because the official `bw` CLI takes about 3 s per
call. `bw` is still needed for exactly one value, the `keystore.p12`
attachment, which `rbw` cannot download (upstream issue doy/rbw#130).

```bash
# sops, pinned: kluster refuses any other version (versions.sops in kluster.yaml)
#   https://github.com/getsops/sops/releases/tag/v3.13.3
sops --version

# rbw, under its own profile, so kluster never shares an agent with daily use
export RBW_PROFILE=kluster
rbw config set email <the Bitwarden account>   # then `rbw register` and `rbw login`
rbw config set lock_timeout 900                # the agent locks itself after 15 minutes
rbw unlock

# refuse commits of unencrypted or unlisted *.enc.yaml
git config core.hooksPath .githooks
```

The `rbw config` commands are rbw's own; this task did not run them, and rbw's
README is the authority. What was proven on 2026-10-20 is that `kluster secrets
list` and `pull --apply` ran through rbw and read all four issued values.

kluster reads the rbw profile `kluster` (its files are in `~/.config/rbw-kluster`),
apart from the default profile, so `RBW_PROFILE=kluster rbw unlocked` is the
check that matters. A profile on a fresh state can fail at login with
`New device verification required`; `rbw` shows it as `failed to log in to
bitwarden instance: api request returned error: 400`. On 2026-10-20 the
default profile failed this way, and `rbw register` fixed it; the same step is
expected to fix the `kluster` profile (assumed, not run there). kluster only warns when `rbw sync` fails and then reads
the local copy, so an item stored in the web vault can show as "not found"
until the sync works.

Three rules follow from the trade-off of trusting a second client. kluster
never unlocks the agent: the operator unlocks it in their own terminal before
running kluster, and kluster fails with the exact unlock command when it is
locked. kluster strips every `RBW_*` and `BW_*` variable from the environment
of the processes it starts, so Terraform and kubectl children cannot reach the
vault. And kluster looks an item up by its exact name, because `rbw get` falls
back to a partial match: it lists the ids once, matches the name, and refuses a
duplicate. For the attachment, unlock `bw` in the same shell (`BW_SESSION`).

The Bitwarden items the catalog names are `kluster/*` (for example
`kluster/doma`, `kluster/authos-keystore`); the age key is `kluster/age-key`.
`kluster secrets list` reports an issued key whose value differs from its
Bitwarden item, and a missing item.

## 3. Everyday use

All writes run in Plan Mode, which prints the plan and writes nothing, unless
given `--apply`. No command prints a value. Commands that decrypt or read the
vault need the unlocked agent, so the operator runs them in their own terminal.

| Task | Command |
|---|---|
| See every key, its kind and its state | `go run . secrets list` (`--no-bitwarden` skips the vault; `--attachments` also compares attachments, slower) |
| New Secret | add the entry to `secrets.yaml`, then `generate`, `pull` or `set` for its keys, then `check` |
| Make every missing generated value, and rewrite every copy and literal | `generate --apply` |
| Give generated keys a new value, and every copy of them in the same write | `rotate <namespace>/<name>.<key>…` or `rotate --all`, with `--apply` |
| Take an issued value from Bitwarden | `pull [<namespace>/<name>.<key>…] --apply` |
| Type an issued value on the terminal, no echo | `set <namespace>/<name>.<key> --apply` (the same value goes into Bitwarden by hand; kluster never writes the vault) |
| Edit one file in `sops edit` | `edit <namespace>/<name> --apply` (for what the commands above do not cover) |
| Does the cluster match git? | `diff [<namespace>/<name>…]` (compares key names; exit status 1 when they differ) |
| Apply | `apply [<namespace>/<name>…] --apply` |
| Prove every file is listed and fully encrypted | `check` (`--staged` for the next commit; needs no key and no sops) |

An issued key is rotated at its provider: issue the new value there, store it
in Bitwarden, then `pull`. Rotating a generated key rewrites its copies in the
same write, which is why `authos/credentials` can never hold a stale database
or Redis password after a `rotate`. kluster writes `data`, never `stringData`,
and re-encrypts the whole file on any change, so a commit shows the file
changed as a whole. `diff` and `apply` act on the live cluster only; with
`--env rehearsal` they refuse.

**Check a value without reading it** by its length in the pod, not by its
base64 length (40 and 41 bytes both encode to 56 characters):

```bash
kubectl exec -n redis deploy/redis -- sh -c 'printf "%s" "$REDIS_PASSWORD" | wc -c'
```

## 4. Applying, and what restarts

`apply` sends each drifting Secret server-side as field manager `kluster` and
stops at the first error. With no names it works in catalog order, which is
alphabetical by namespace and **not** by dependency, so name the Secrets in the
order they should land. The order used for the full rotation on 2026-10-20 was
sources first, then the copies, then the rest: `pg-cluster/db-credentials`,
`redis/redis`, `authos/credentials` with `doma/credentials`, then
`cert-manager/cloudflare-dns-token` and `argocd/argocd-notifications-secret`.
Rotate a source and its copies in the same commit and the same apply.

**A Secret first made with `kubectl create` is owned by the field manager
`kubectl-create`, and kluster stops with** `conflict with "kubectl-create"
using v1: .data.password`. Add `--force-conflicts` to the first apply of that
Secret (the flag comes with PR #37); later applies need no flag, and it is off
by default. It was chosen over deleting and re-applying, because a Secret
briefly absent can be reconciled by CloudNativePG at the wrong moment.

🔴 **A forced apply does not remove a key that only the old manager owns.**
`apply` then ends with "the live Secrets still differ after apply". The case
met was `DB_USER` on live `authos/credentials`, which moved to the
`authos-api-config` ConfigMap. ConfigMaps are applied by hand, so the live one
did not have the key yet. The order that kept the API up: apply the ConfigMap,
restart the Deployment, check the value in the pod, and only then remove the
key from the Secret.

```bash
kubectl apply -f projects/authos/api/manifests/configmap.yaml
kubectl -n authos rollout restart deploy/authos-api
kubectl -n authos patch secret credentials --type=json -p '[{"op":"remove","path":"/data/DB_USER"}]'
```

Reloader ([`core/reloader`](../../core/reloader/README.md)) restarts the
readers after any change to a Secret, whoever made it. A Deployment opts in
with `secret.reloader.stakater.com/auto: "true"` in its base `deployment.yaml`;
today `redis`, `authos-api`, `duster` and `doma` carry it. It watches only the
namespaces listed in `core/reloader/reloader-values.yaml` (`redis`, `authos`,
`doma`), so **a new reader needs the annotation and its namespace in the
values**, followed by `core/reloader/render.sh` and a re-apply, or its Secret
change is never seen. Two readers are not covered, because they read their
Secrets themselves: CloudNativePG (`pg-cluster/db-credentials` carries the
`cnpg.io/reload` label, and without it a rotation is silently ignored),
cert-manager and ArgoCD.

Proof on 2026-10-20: after the rotation the Reloader log showed `Changes
detected in 'redis' of type 'SECRET'` and an update of the Deployment, the same
for `authos-api`, `duster` and `doma`; `kluster secrets diff` then listed all 13
Secrets as in step and exited 0.

## 5. Backup drill — every three months, and after any key change

Proves the backup key still decrypts. A backup nobody has tested is not a
backup.

```bash
# on a machine with the backup key, NOT the primary
SOPS_AGE_KEY_FILE=<path to the backup key> \
  sops decrypt --extract '["data"]["keystore.p12"]' \
  projects/authos/api/manifests/keystore.enc.yaml | base64 -d | wc -c
```

Pass: a non-zero byte count. Remove the key file from that machine afterwards.

## 6. Adding or removing a recipient

```bash
$EDITOR .sops.yaml                    # add or remove a public key under `age:`
sops updatekeys -y <every *.enc.yaml> # re-wraps the data key; values are unchanged
go run . secrets check
```

`updatekeys` is a plain `sops` run, outside kluster, and needs a private key
that can already decrypt. Removing a recipient this way does **not** protect
old commits: see section 7.

## 7. 🔴 If a private key leaks

Assume every value ever committed is readable: the old ciphertext stays in
the public history, and no later commit changes that. New keys alone fix
nothing; **the values themselves must change.**

1. Make a new age key, store it in `kluster/age-key`, and replace the leaked
   recipient in `.sops.yaml`.
2. `sops updatekeys -y` then `sops rotate -i` on every `*.enc.yaml`: new data
   keys, readable only by the new recipients.
3. **Change every real value.** `rotate --all` renews every generated key and
   every copy of it. Each issued key is issued again at its provider, stored in
   Bitwarden, then `pull`ed. Two need care:
   - `authos/duster-admin` (generated): `dstr` and `authos-demo/scripts/bootstrap.ts`
     must use the same new value.
   - **the authos keystore** (issued, an attachment and its password in
     `kluster/authos-keystore`). A new signing key means every token Authos has
     issued stops verifying, and every user signs in again. A new
     `authos-credentials-encrypt` key means anything encrypted with the old one
     is unreadable: plan that migration before rotating it.
4. `apply` every changed Secret, in the order of section 4. Reloader restarts
   the readers; confirm each one is new and Ready. Commit the files by name.

## 8. What is deliberately NOT done

- **No `mac_only_encrypted`.** The default MAC covers the unencrypted fields
  too, so a changed `name` or `namespace` fails decryption instead of applying
  a Secret somewhere else.
- **No guard that works after the push.** The pre-commit hook (`kluster
  secrets check --staged`) is the guard, and it works only where
  `core.hooksPath` is set. The CI job `secrets-check.yml` runs the same check
  on every pull request, which catches a clone that never enabled the hook,
  but a plaintext file on a pushed branch of a public repository is already
  public: CI reports it, it does not undo it.
- **No post-quantum recipient yet.** SOPS parses hybrid `age1pq1…` recipients,
  but age calls them experimental. Revisit when that changes: the
  ciphertext here is public for ever, which is exactly the "collect now,
  decrypt later" case they exist for.
- **No ArgoCD plugin.** Secrets are applied out of band, like every other
  object that holds a value set once ([ADR 0003](../adr/0003-recoverability.md)).
