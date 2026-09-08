# Deploy-live notifications & the version catalog — how it works

This is the architecture reference for the deploy-notify + version-catalog flow.
For day-to-day operations (adding an app, replaying an event, the required secrets)
see [`README.md`](README.md). For the ArgoCD side see
[`../core/argocd/notifications/README.md`](../core/argocd/notifications/README.md).

## What it does

When an app version is **actually running and healthy** in the cluster, the flow
produces three durable records and one ping:

| Output | Where |
| --- | --- |
| append-only deploy log | `deployments/history.jsonl` |
| readable roll-up | `deployments/CATALOG.md` (regenerated, never hand-edited) |
| native GitHub Deployment | the app repo's Environments / Deployments tab |
| "it happened" message | Telegram (infra bot) |

It is **additive**. The deploy mechanism is unchanged: the image tag is still
`alpha-<git-sha>` and the ArgoCD overlay still sets `newTag: alpha-<git-sha>`.
Semver tags, GitHub Releases, `CHANGELOG`, this catalog, and the Telegram ping are
metadata layered on top. Turning this flow off changes nothing about how apps deploy.

## The signal boundary

The true "app X version Y is live" signal is **ArgoCD reporting the `Application`
`Synced` + `Healthy` on the new image**. That is a cluster fact, so the cluster
emits it (ArgoCD Notifications) and this repo receives it (a GitHub Action). The app
repos know nothing about notifications; they only push an image and bump the overlay
as they always did.

## End-to-end

```
  app repo: PR merged to main
        │
        ├─► release-please ──► "chore(main): release X.Y.Z" PR
        │                      merging THAT PR tags vX.Y.Z + cuts a GitHub Release
        │
        └─► the app's deploy workflow
              ├─ build   (git describe → APP_VERSION build-arg → image alpha-<sha>)
              └─ deploy  (kustomize edit set image → commit to THIS repo)
                     │
                     ▼
              ArgoCD syncs the app, waits for Healthy
                     │
                     ▼
        argocd-notifications-controller  (in-cluster)
          trigger "on-deployed":
            operationState.phase == Succeeded
            health.status        == Healthy
            sync.status          == Synced
          oncePer: app.status.summary.images     ← fires once per running image set
                     │
                     │  POST https://api.github.com/repos/stevetosak/hetzner-cloud-infra/dispatches
                     │       Authorization: token <argocd-notifications-secret.github-token>
                     ▼
        repository_dispatch  { event_type: "deploy-live",
                               client_payload: { app, project, revision, image } }
                     │
                     ▼
        .github/workflows/deploy-catalog.yml   (this repo, on master)
          1. assemble payload; look up apps.json[app].repo
          2. clone the app repo (blobless, no-checkout) for its tags
          3. resolve.mjs  → git describe at the deployed sha → a Row
          4. record.mjs   → append to history.jsonl (idempotent on app+sha)
                          → regenerate CATALOG.md
          5. commit  "chore: catalog <app> <version> [skip ci]"  (parallel-safe loop)
          6. GitHub Deployment on the app repo   (APP_DEPLOY_PAT)
          7. Telegram message                    (INFRA_TELEGRAM_*)
```

## Components

### 1. ArgoCD Notifications — `core/argocd/notifications/`

Two cluster objects in the `argocd` namespace:

- **`argocd-notifications-cm`** — trigger, template, webhook service, subscription.
- **`argocd-notifications-secret`** — key `github-token`, a fine-grained PAT with
  **Contents: Read and write** on this repo (that is all `POST /dispatches` needs).

`argocd-notifications-controller` (ships with ArgoCD) watches every `Application` and
re-evaluates the triggers on a loop.

**Trigger `on-deployed`** — the `when` clauses are the definition of "up and serving":

| Clause | Meaning |
| --- | --- |
| `operationState.phase == Succeeded` | the last sync finished with no error |
| `health.status == Healthy` | Deployment available, pods Ready, **readiness probes pass** |
| `sync.status == Synced` | live state matches git |

**`oncePer: app.status.summary.images`** — fire once for each distinct value of the
running image list. A new deploy changes the list → one fire. The controller records
"already sent" as a `notified.notifications.argoproj.io` annotation on the `Application`,
keyed by a hash of `{image set : trigger : recipient}`.

> Do **not** use `oncePer: app.status.sync.revision`. Every `Application` here tracks
> this repo's `HEAD` with no `manifest-generate-paths` annotation, so any commit to
> this repo — including the catalog's own `chore: catalog …` commit — advances
> `sync.revision` for all apps at once. That fans out to one dispatch per app per infra
> commit, and the catalog commit re-triggers the whole set. `summary.images` only
> changes when that one app's image changes.

**Webhook service `gh-infra`** — a named `POST` target:
`https://api.github.com/repos/stevetosak/hetzner-cloud-infra/dispatches`, header
`Authorization: token $github-token` (substituted from the secret).

**Template `app-deployed`** — renders the `POST /dispatches` body:
`{ event_type: "deploy-live", client_payload: { app, project, revision, image } }`.
`image` is `{{index .app.status.summary.images 0}}` — the sha rides inside its
`:alpha-<sha>` suffix.

**Subscription** — a single **global** entry in the cm binds `on-deployed → gh-infra`
for every `Application`. No per-`Application` annotations, so a later ApplicationSet
migration (which regenerates the `Application` CRs) leaves it intact.

> Quirk: this controller version writes the `notified` annotation **even when the
> webhook POST fails**. A burst that 401s (e.g. a bad token) will not auto-retry —
> each app stays deduped until its image next changes. To force a replay, clear the
> `notified.notifications.argoproj.io` annotation on the affected `Application`s.

### 2. Transport — `repository_dispatch`

`POST /repos/{owner}/{repo}/dispatches` with an `event_type` + `client_payload` makes
GitHub emit a `repository_dispatch` event. `deploy-catalog.yml` listens for
`types: [deploy-live]` and reads `github.event.client_payload`.

Constraints:
- `repository_dispatch` only triggers workflows **on the default branch** (`master`).
- A bad PAT is a silent `401` on the ArgoCD side — visible only in the controller log
  (`kubectl -n argocd logs deploy/argocd-notifications-controller`).

### 3. Worker — `.github/workflows/deploy-catalog.yml`

One job. Triggers: `repository_dispatch: [deploy-live]`, or manual `workflow_dispatch`
with a `payload` input (replay). All logic is in three dependency-free Node modules
with `node:test` coverage.

| Step | Module | What it does |
| --- | --- | --- |
| assemble payload | — | normalize `client_payload` vs the replay input; look up `apps.json[app].repo` |
| clone app repo (`if repo != ''`) | — | `git clone --filter=blob:none --no-checkout` + `git fetch --tags` — just for `git describe` |
| build the row | `resolve.mjs` | `parsePayload` → `{app, image, sha, revision}`; `describeVersion` runs `git describe --tags --always --match '<tagPrefix>[0-9]*'` at the sha; append `deployed_at` |
| record + regenerate | `record.mjs` | `recordRow` appends to `history.jsonl` **idempotent on app+sha**; regenerate `CATALOG.md` via `render-catalog.mjs`; print `appended` / `duplicate` |
| commit (`if appended`) | — | `chore: catalog <app> <version> [skip ci]` + the parallel-safe push loop |
| GitHub Deployment (`if repo != ''`) | — | `POST /repos/{app}/deployments` (`environment: dev`, `required_contexts: []`, `auto_merge: false`) + a `success` status → the app repo's Environments tab |
| Telegram (`if: always() && appended`) | — | `POST .../sendMessage` with `chat_id` + `Deployed: <app> <version> (<sha7>)` |

**`describeVersion` results:** `v0.3.0` on a release commit · `v0.3.0-5-gabc1234`
between releases · `<tagPrefix>0.0.0+<sha7>` when no matching tag exists ·
`unknown` when `apps.json[app].repo` is `null`. Never throws.

**Parallel-safe commit loop** (no `concurrency:` guard — serializing would let one slow
deploy block the catalog for another):

```
for i in 1..5:
  git fetch origin master
  if HEAD^ == FETCH_HEAD:            # upstream unchanged
      git push  → done
  else:                             # upstream moved
      git reset --hard FETCH_HEAD   # drop our commit
      node record.mjs "$ROW"        # re-append onto the new tip (idempotent)
      git commit ; git push  → done
```

`CATALOG.md` is derived — never merged, always regenerated. `.gitattributes` marks
`deployments/history.jsonl merge=union` so even a straight merge of two parallel
appends keeps both lines.

**`APP_DEPLOY_PAT`:** fine-grained, **Deployments: Read and write** on the app repos.
Unset → the Deployment step warns and skips (catalog + Telegram still run). Expired →
the step fails (red run) but the catalog commit and Telegram still land.

**`chat_id`** is a numeric id, not a username. A bot sends *to* a chat and cannot
message a user who never pressed Start on it.

### 4. The version string — `git describe` (in the app repos)

`git describe --tags --always --match 'v[0-9]*'` never lags: `v0.3.0` on the tagged
commit, `v0.3.0-N-g<sha7>` N commits later, a bare sha before the first tag. The app's
deploy workflow checks out with `fetch-depth: 0` (shallow clones carry no tags), runs
`git describe`, and passes it as `--build-arg APP_VERSION=…`. The Dockerfile turns
that into `ENV APP_VERSION`, surfaced by the app at `/api/health`. The **image tag
stays `alpha-<full-sha>`** — the semver rides inside the image as an env var, and the
catalog resolves it again independently from the app repo's tags.

### 5. release-please (in the app repos)

Runs on every push to the app's `main`. Two phases, one workflow:

1. **Release-PR maintenance** — parse commits since the last release tag as
   Conventional Commits, compute the next version, open/update a
   `chore(main): release X.Y.Z` PR that bumps the manifest + writes `CHANGELOG.md`.
   Non-conventional and merge commits fail to parse and are skipped (harmless).
2. **Release creation** — when that PR merges, the workflow re-runs on the release
   commit, creates the tag `vX.Y.Z` and a GitHub Release.

Fully additive: it only touches the manifest / changelog / tags / releases. The tag it
creates is what `git describe` picks up on the **next** deploy. Requires the app repo
setting *Allow GitHub Actions to create and approve pull requests*.

## "What ran where, when" — the three surfaces

| Surface | What it is | Written by |
| --- | --- | --- |
| `deployments/history.jsonl` | append-only source of truth, one JSON object per deploy, `merge=union` | `record.mjs` only |
| `deployments/CATALOG.md` | rendered view: "Current" (latest per app) + "Recent history" (last 20) | regenerated, never by hand |
| GitHub Deployments (per app repo) | native GitHub UI, `environment: dev`, links to the live host | `deploy-catalog.yml` |

The Telegram message is the ephemeral ping; the three above are the record.

## Failure modes

| Failure | Behaviour |
| --- | --- |
| ArgoCD `github-token` invalid | `401`; controller marks `notified` anyway, no retry. Fix the token; clear the annotation to replay. |
| Two deploys race on the catalog commit | fetch/reset/re-record loop converges; `merge=union` on `history.jsonl` is the backstop. |
| `APP_DEPLOY_PAT` unset | Deployment step warns + skips; catalog + Telegram run. |
| `APP_DEPLOY_PAT` expired | Deployment step fails (red run); catalog commit + Telegram still land. |
| Telegram secret unset | Telegram step warns + skips; everything else runs. |
| non-conventional commit subjects | release-please skips them; the version bump still computes. |
| `git describe` vs release-please tag timing | one commit's version string may read `vX.Y.(Z-1)-N-g<sha>`; self-corrects next commit. |
| dispatch replayed manually | `history.jsonl` is idempotent on `app+sha` — no double record. |
| ApplicationSet migration later | global `subscriptions` entry survives; nothing per-`Application` to lose. |

## Verified end-to-end (2026-09-08, `doma`)

- `doma` PR merged → `deploy` built `APP_VERSION=v0.2.0-95-g8324916`, pushed
  `alpha-8324916…`, bumped this repo's overlay.
- `doma.tosak.net/api/health` → `{"status":"ok","version":"v0.2.0-95-g8324916"}`.
- ArgoCD `on-deployed` fired once → `deploy-catalog.yml` succeeded:
  `history.jsonl` + `CATALOG.md` row, GitHub Deployment on `stevetosak/doma`
  (env `dev`, `success`), Telegram delivered.
- `release-please` opened the `v0.3.0` PR; merging it tagged `v0.3.0`. The next deploy
  resolved `git describe` to exactly `v0.3.0` — `/api/health` now reports `v0.3.0`,
  and `CATALOG.md` shows the `v0.3.0` row.
