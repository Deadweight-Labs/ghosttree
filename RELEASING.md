# Releasing Ghosttree

Ghosttree uses Semantic Versioning and a fixed promotion path. Release tags are
immutable; a bad release is followed by a new version, never a moved tag.

## Version policy

- `major`: incompatible behavior after 1.0.
- `minor`: compatible functionality. Before 1.0, this also covers incompatible
  behavior.
- `patch`: compatible fixes.
- `none`: tests, documentation, and internal changes with no release impact.

The first planned promotion is `v0.1.0-rc.1`, followed by `v0.1.0`.

## Branches

- `dev` is the protected integration branch.
- Feature, fix, documentation, test, and chore branches merge into `dev` by
  reviewed pull request.
- `release/X.Y.Z` is cut from a green `dev` and receives release-only fixes.
- `main` contains final releases and is protected from direct pushes.
- `hotfix/X.Y.Z` starts from `main`, returns to `main` by pull request, and is
  backmerged into `dev`.

## Release candidate

1. Confirm `dev` is green and choose the SemVer impact from merged pull
   requests.
2. Create `release/X.Y.Z` from `dev`.
3. Update the changelog and release metadata on that branch.
4. Run the full local verification and wait for required CI checks.
5. Create an annotated tag `vX.Y.Z-rc.N` at the reviewed release commit.
6. Push the branch and tag. The release workflow publishes it as a prerelease.

Additional candidates increment `N`; an RC tag is never reused.

### Release-named context marks

When recording a Ghosttree context snapshot with the release tag as its name,
create the local tag first and verify the exact checkout rather than relying on
a branch name:

```bash
test -z "$(git status --porcelain=v1)"
test "$(git rev-parse 'refs/tags/vX.Y.Z-rc.N^{commit}')" = \
     "$(git rev-parse 'HEAD^{commit}')"
ctx snapshot create vX.Y.Z-rc.N -m 'Release context mark'
ctx snapshot verify vX.Y.Z-rc.N
```

Annotated tags are peeled to their commit for this comparison. A tag mismatch
must not be bypassed; `--allow-dirty` overrides only the dirty-worktree refusal
and is not part of the normal release path. The snapshot timestamp records when
the mark was actually created and does not claim that Ghosttree data travelled
back in time with the Git checkout.

## Final release

1. Confirm the chosen release-candidate commit is green and approved.
2. Merge `release/X.Y.Z` into `main` by pull request.
3. Create the annotated tag `vX.Y.Z` on the resulting `main` commit.
4. If a release-named context mark is required, perform the clean-checkout and
   peeled-tag checks above, then create and verify it.
5. Push the tag and approve the run in the environment `release` (see below).
   The workflow creates the release as a **draft**, fetches the uploaded files
   back, checks `checksums.txt.minisig` against the committed key and every
   archive against the signed list, and only then publishes. A failed check
   leaves a draft to delete; fix the cause with a new tag. Verify archives, SBOMs
   and signatures by hand if you want a second look.
6. Merge `main` back into `dev` and remove the release branch.

Stable release artifacts are built only from final tags. A failed publication
is corrected with a new tag and version.

## Required repository settings

GitHub branch protection is part of the release boundary and must be configured
for both `dev` and `main`: pull requests, required reviews, required status
checks, resolved conversations, no force pushes, and no branch deletion. Limit
release-tag creation and release approval to maintainers.

## Signing key

Every release is signed twice: with a minisign key (`checksums.txt.minisig`),
which `install.sh`, `ctx update` and `ctx server update` check, and keyless with
Sigstore (`checksums.txt.sigstore.json`), for people who want an independent
second check by hand. The minisign signature covers `checksums.txt`, which lists
the SHA-256 of every archive, and its trusted comment is `ghosttree <tag>`, so
the signed list of one release cannot stand in for another.

- The public key is `deploy/downloads/ghosttree/minisign.pub`, the
  `pubkey='...'` line of `deploy/downloads/ghosttree/install.sh` and
  `internal/releasekey/minisign.pub` (built into ctx). It is also written into
  `SECURITY.md`, as a second source that does not depend on the download host;
  the release workflow stops unless `SECURITY.md` carries the same key. Until the
  key exists these hold a placeholder: `install.sh` and `ctx update` refuse to
  check anything and the release workflow stops before it publishes.
- **The secret key has a password** (`minisign -G`, not `-W`). The key file lives
  in Bitwarden (vault dwl, "ghosttree release signing key (minisign)") and as the
  secret `MINISIGN_SECRET_KEY` of the GitHub **environment `release`**, the
  password as `MINISIGN_PASSWORD` of the same environment (and in Bitwarden under
  its own entry). Neither is a repository secret. A copy of the key file without
  the password signs nothing.
- **The environment `release` must be set up as follows** (GitHub, Settings,
  Environments; this is part of the release boundary, not optional):
  required reviewers (at least one maintainer who did not push the tag),
  "Prevent self-review" on, deployment branches and tags limited to `v*`, and
  no administrator bypass. With that, a run that has the key waits for a person to
  approve it: a branch, a pull request or another workflow never reads the key, and
  neither does a pushed tag nobody approved.
- **Tags:** a repository ruleset allows creating and changing `v*` tags only for
  maintainers and forbids deleting and moving them. The password in the same
  environment protects the key file against leaking out of a backup or a log, not
  against a workflow that runs inside the approved environment; the reviewers and
  the tag rules are what keep a hostile workflow file from being approved.
- The workflow reads the secrets in exactly one step, the one right before
  goreleaser. That step writes the key file, signs a probe with the password and
  refuses to go on if the probe does not verify against `minisign.pub`. The step
  after goreleaser deletes the file, whatever the result. The actions that run
  earlier (Go, Node, Syft, Cosign) never see the key, and `actions/checkout` runs
  with `persist-credentials: false`, so the job token is not left in
  `.git/config`. `internal/installscript/workflow_test.go` and
  `release_draft_test.go` check all of this on every change to the workflow.
- Set up or rotate: generate a pair with a password, update Bitwarden and the two
  environment secrets (TASK-159), then run
  `scripts/release-key.sh <new .pub file>`, put the new key line into
  `SECURITY.md`, and merge the changed files. Installers fetched before a rotation
  refuse releases signed with the new key and say so; people fetch the script
  again. A ctx built before the rotation cannot check a release signed with the
  new key either; it is updated with the installer.
- Verify by hand: `minisign -Vm checksums.txt -p minisign.pub`, then
  `sha256sum -c checksums.txt --ignore-missing`.

### What the installer's checks do not cover

Say these next to the one-line install command, so nobody reads more into the
signature than it gives:

- **No protection against freezing.** `install.sh` asks GitHub's API (unsigned)
  which release is the newest. Someone who can cut that connection, or serve an
  old answer, keeps a machine on an older release that is still genuine and still
  signed: a minisign signature carries no date. `install.sh` and the updates do
  refuse a release older than the ctx that is installed (`--allow-downgrade`
  overrides), so a machine is not moved back, but one that has no ctx yet can be
  given an old release. Name the release you want with `--version vX.Y.Z`
  (compare it with the releases page) when that matters.
- **`curl | sh` trusts the host it comes from.** The key is in the script, so the
  signature protects against a swapped GitHub asset or mirror, not against a
  compromised download host or its certificate, which could swap script and key
  together. Compare the key in the script with the one in `SECURITY.md` on GitHub,
  or download `install.sh` from the release itself. The
  signed comment `ghosttree <tag>` makes sure a list of one release is never taken
  for another; it cannot say which release is the newest.
- **Releases from before the signature cannot be installed this way.** A release
  made before this pipeline has no `checksums.txt.minisig`; `install.sh` and
  `ctx server update` refuse it ("a release without a signature is not
  installed"), and a `ctx` from that time does not know `ctx server` at all. Such a
  setup is updated by hand (deploy/README.md) to the first signed release; from
  then on `ctx server update` takes over.
- **macOS needs minisign.** The fallback checker is OpenSSL 3, and macOS ships
  LibreSSL, which cannot check an Ed25519/BLAKE2b signature. Without
  `brew install minisign` the installer stops with that instruction and installs
  nothing. Linux machines with OpenSSL 3 (Debian 12, Ubuntu 22.04 and newer,
  Fedora) need nothing extra.
- **Downloads go over https only** (curl, or GNU Wget 1.x with every redirect
  checked by the installer). BusyBox wget and Wget2 (Fedora's `wget`) cannot be
  stopped from following a redirect to plain http and are refused: install curl.

## Docker image

The release workflow builds `ghcr.io/deadweight-labs/ghosttree` from the
`Dockerfile` for linux/amd64 and linux/arm64 before it publishes the release,
tagged `X.Y.Z` (and `latest` for a final release, not for a release candidate),
with provenance and an SBOM, and signs the digest keyless with cosign. The
workflow checks that the pushed image runs and reports the version of the tag.
The pull happens with the workflow's own token, so the package has to allow
Actions of this repository to write to it (package settings on GitHub, "Manage
Actions access"); a new package is private until it is made public there.
`deploy/downloads/ghosttree/compose.yaml` is also a release asset. The base and
build images in the `Dockerfile` are pinned by digest; update them with the
Go version of `go.mod`.
