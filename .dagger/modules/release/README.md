# release

Builds, labels and publishes the repo's container images, and tests the
release scripts that tag them.

| Function | What it does |
| --- | --- |
| `publish` | Build the image a release tag names and push it to GHCR |
| `image` | The same image, labelled, without publishing it |
| `build-dockerfiles` | Check: build every `Dockerfile` in the repo |
| `test-scripts` | Check: run `scripts/tests` against the release scripts, this module's `releases.json` and the cliff configs |

Devenv images come from the devenv toolchain's `container`, so a published
`devenv-ci` image is built from exactly what `dagger check` runs in.

## Container releases

`releases.json` maps each project to its working directory, git-cliff
config, and artifact prefixes. Only each project's include paths in its cliff.toml
trigger releases; changes to shared release tooling do not bump projects. The first artifact is the canonical version
stream; a single bump creates every artifact tag at the same commit. Root
`cliff.toml` includes historical `devenv-*` tags to preserve existing versions.
Devenv publishes `devenv-ci-<version>` with `ci` and
`devenv-shell-<version>` with `ci` plus `interactive`. Forgejo resources starts
at `forgejo-resources-1.0.0` and uses its own cliff.toml. The OpenHands sandbox
starts at `openhands-sandbox-0.2.0`, likewise with its own cliff.toml.

`scripts/release.sh` creates Forgejo releases on main pushes. The shared tag
pipeline invokes `dagger api call release publish --tag=<tag> --username=teekennedy
--token=env:GHCR_ACCESS_TOKEN`, selecting profiles or a Dockerfile build from
the manifest. Images use `ghcr.io/teekennedy/k8s-homelab:<tag>`.
When adding projects, also extend the push path and tag ref filters in
`.woodpecker/release-tag.yaml` and `.woodpecker/release.yaml`.

`dagger api call release build-dockerfiles` discovers every `Dockerfile` and builds it with
its containing directory as context. It runs in `dagger check` and is also
available as `build-containers` in devenv. Each directory retains an independent
build cache.

### Image metadata

All images label their title, description, source, URL, documentation, license,
version, and revision. The source is the public GitHub mirror and the license is
MIT. Local Dockerfile builds default to version `dev` and revision `unknown`;
set the `VERSION` and `REVISION` build arguments for manual releases.

The tag publishing pipeline runs `scripts/release-metadata.sh <tag>` in a full
clone, then supplies the JSON to `release publish --metadata=<file>`. Metadata
contains the exact tagged commit, its committer timestamp (for reproducible
`org.opencontainers.image.created` values), and git-cliff notes for that release.
Both devenv artifacts use the canonical CI tag's notes. Metadata generation uses
an explicit commit range from the previous project tag, including legacy prefixes
listed in the manifest, to the requested commit. It also supports existing tags
with no relevant commits (an empty changelog). The script rejects a
checkout that does not match the requested tag and its canonical sibling.

Publishing preserves existing title, source, URL, documentation, and license
labels, using defaults only for missing labels. An existing description supplies
the base text for the release-note preview. Version, revision, creation time,
and changelog labels describe the release.

GitHub renders `org.opencontainers.image.description` as plain text and limits
it to 512 characters. Released images use a stricter 512-byte UTF-8 budget,
including the ellipsis, for the base description and truncated plain-text
changelog preview. The complete notes remain in
`io.github.teekennedy.homelab.changelog`, and
`org.opencontainers.image.documentation` defaults to the Forgejo release when
the image has no documentation label. Standard
OCI keys are also emitted as manifest annotations by Dagger.

Inspect a release's metadata without publishing:

```sh
scripts/release-metadata.sh "$TAG" > /tmp/release-metadata.json
dagger api call release image --tag="$TAG" --metadata=/tmp/release-metadata.json \
  label --name=org.opencontainers.image.description
```
