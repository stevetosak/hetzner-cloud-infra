---
title: "Make the repository private on GitHub Pro"
description: "What has to change before hetzner-cloud-infra can go private without breaking a deploy, an ArgoCD sync or a docs build, and the order of the switch."
type: plan
status: abandoned
topics: [ci, gitops, docs, security]
---

**Abandoned on 2026-11-08: the repository stays public.** The gain is small. The exploitable code is in the application repositories, which stay public; the history and docs.tosak.net already show the cluster's layout; and the switch would cost GitHub's free secret scanning and push protection and add two new credentials. The time from a published fault to a patched cluster matters more than hiding the manifests. A client's application may be open or closed source by the client's own choice, so client confidentiality is not a reason either. The plan below stays as it was written, as the record of what a switch would take.

The repository `stevetosak/hetzner-cloud-infra` has been public since it was
created. The operator decided on 2026-11-08 that it goes private, on a GitHub
Pro account. Four things depend today on anyone being able to read it, and each
one breaks on the day the visibility changes unless it is prepared first.

**On GitHub Free a private repository has no rulesets**, so `master-protection`
would stop requiring a pull request and the `deploy-scope` check, and
`tag-bump`'s call to `enablePullRequestAutoMerge` would fail on every deploy.
This was proven on the account's private `tosak-docs`: `GET /rulesets` answers
403, "Upgrade to GitHub Pro or make this repository public". Pro keeps the
rulesets and auto-merge, and it raises the Actions allowance from 2,000 to
3,000 minutes a month; the last 30 days used about 490 minutes, summed from run
durations (an estimate, since billing rounds each job up).

The other three dependencies do not care about the plan. The public app
repositories call the composite action
`stevetosak/hetzner-cloud-infra/.github/actions/tag-bump@master` (authos in
four workflows, doma in one), and an action in a private repository can only be
shared with other private repositories. ArgoCD reads the repository over
anonymous HTTPS: the `argocd` namespace has no Secret with
`argocd.argoproj.io/secret-type=repository`. The Cloudflare build of
docs.tosak.net clones every outside source without a token
(`tosak-docs/scripts/prepare-sources.ts`), and a failed clone stops the whole
site build, not only this project's pages.

Making the repository private does not take back what was public. The SOPS
ciphertext and the history of the cluster's layout may already be copied
elsewhere; the age key never left the password manager, so the secrets stay
safe. The repository has no forks, so no public copy remains on GitHub.

## Phase 1 — Prepare while the repository is public

**Done when:** the account is on Pro; no repository outside this one calls an
action from it; ArgoCD reads the repository with its own read-only credential;
the docs build clones it with a read-only token; and gitleaks scans every
commit in the pre-commit hook and in CI. Each of these is proven before Phase 2
starts.

- [ ] Upgrade the account to GitHub Pro. Proof: `GET
      /repos/stevetosak/tosak-docs/rulesets` answers 200 instead of 403.
- [ ] Move `.github/actions/tag-bump` (with `tag-bump.test.mjs` and its CI)
      to a new public repository, `stevetosak/tosak-actions`, and point the
      authos and doma workflows at it, pinned to a commit. Remove the action
      from this repository and update the `app-ci` rule of the tosak-platform
      module. Proof: one doma deploy and one authos deploy land through the
      new action.
- [ ] Give ArgoCD a read-only credential: a GitHub App with Contents: Read,
      installed on this repository only, as a `repo-creds` Secret in `argocd`
      for `https://github.com/stevetosak` (`githubAppID`,
      `githubAppInstallationID`, `githubAppPrivateKey`), listed in
      `secrets.yaml` first. An App keeps the HTTPS `repoURL` in the
      ApplicationSets and AppProjects as it is and never expires; a deploy key
      would move every `repoURL` to SSH, and a fine-grained PAT expires.
      Proof: `argocd repocreds list` shows the prefix, and a hard refresh of
      one Application succeeds with it.
- [ ] Give the Cloudflare docs build a fine-grained token with Contents: Read
      on this repository only, and make `prepare-sources.ts` pass it through
      `GIT_CONFIG_*` as an `http.extraHeader`, so it is never part of a URL or
      a log line. Correct the comment there and the Style Guide sentence
      "Every project repository is public". Proof: a build with the token
      succeeds while the repository is still public.
- [ ] Add gitleaks in place of GitHub's secret scanning and push protection,
      which stop with the switch: GitHub gives both free to public
      repositories only, and on a private one they need GitHub Secret
      Protection, sold to organisations on Team or Enterprise Cloud, not to a
      personal account on Pro (GitHub Docs, "Supported secret scanning
      patterns", read 2026-11-08). `kluster secrets check` covers only the
      `*.enc.yaml` files, so a plaintext token in any other file would pass.
      Pin the gitleaks release as `versions.gitleaks` in
      `tools/kluster/kluster.yaml`, beside `sops`. Run it on the staged
      changes in `.githooks/pre-commit` and on the pushed commits in the
      `secrets-check` job; allow-list the `*.enc.yaml` files only if their
      ciphertext gives false hits. Scan the full history once: a real hit is
      already public, so it is rotated, not only allow-listed. Proof: a
      commit with a test token is refused by the hook and by CI, and the
      history scan is clean or every hit is rotated.

## Phase 2 — Switch and prove

**Done when:** the repository is private, and the rulesets, an ArgoCD sync, an
app deploy, the deploy catalog, the Recoverability Watch and a docs build have
each been seen working after the switch.

- [ ] The operator changes the visibility (`gh repo edit
      stevetosak/hetzner-cloud-infra --visibility private
      --accept-visibility-change-consequences`).
- [ ] Prove the switch: `GET /rulesets` still lists `master-protection` and
      `deploy-catalog-protection` as active; every Application is `Synced`
      after a hard refresh; one app deploy runs from push to the
      `on-deployed` dispatch; the next Recoverability Watch run is green; a
      docs build succeeds.
- [ ] Rewrite the text that says the repository is public: the Secrets section
      of `CLAUDE.md` (the ciphertext stays public for ever in the history, so
      that warning keeps its substance), the tosak-platform rules,
      `sources.config.ts` and the comments in `recoverability-watch.yml` and
      `docs-contract.yml`.
