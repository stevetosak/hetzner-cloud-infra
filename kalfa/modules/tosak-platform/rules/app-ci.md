---
paths:
  - ".github/workflows/**"
---
# An app repository's CI on the tosak platform

1. **CI gate.** The repository runs its own tests; nothing ships unless the gate is green.
2. **Build and push.** `docker/build-push-action` pushes `stevetosak/<image>:latest` and `stevetosak/<image>:alpha-<full-sha>`.
3. **Deploy job.** It calls the composite action `stevetosak/hetzner-cloud-infra/.github/actions/tag-bump@master`, which authenticates as the GitHub App `tosak-tag-bump`, opens the PR `deploy/<app>-<sha7>` that changes only `newTag` in the overlay, waits for `deploy-scope` and the auto-merge, and fails the run if the merge does not land in time.

The repository needs the variable `DOCKERHUB_USERNAME` and the secrets `DOCKERHUB_TOKEN` (a Docker Hub access token) and `TAG_BUMP_APP_KEY` (the private key of `tosak-tag-bump`, installed on the app repository and on the infra repository). There is no `INFRA_REPO_TOKEN` and no personal access token.

```yaml
deploy:
  needs: build-push
  runs-on: ubuntu-22.04
  concurrency:
    group: tag-bump-<project>-<component>
    cancel-in-progress: false
  steps:
    - uses: stevetosak/hetzner-cloud-infra/.github/actions/tag-bump@master
      with:
        private-key: ${{ secrets.TAG_BUMP_APP_KEY }}
        overlay-dir: projects/<project>/<component>/manifests/overlays/dev
        image: stevetosak/<image>
        tag: ${{ needs.build-push.outputs.IMAGE_VERSION }}
```

The overlay must be in the `projects` generator list of cloud-infra, or `deploy-scope` refuses the PR.

## Version strings

An app with release-please gets semver from `git describe --tags --always --match 'v[0-9]*'`. The checkout step needs `fetch-depth: 0` and `fetch-tags: true`. The version goes to the image as the build argument `APP_VERSION`.
