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
5. Push the tag and verify archives, checksums, SBOMs, and signatures (the
   workflow already checks `checksums.txt.minisig` against the committed key).
6. Merge `main` back into `dev` and remove the release branch.

Stable release artifacts are built only from final tags. A failed publication
is corrected with a new tag and version.

## Required repository settings

GitHub branch protection is part of the release boundary and must be configured
for both `dev` and `main`: pull requests, required reviews, required status
checks, resolved conversations, no force pushes, and no branch deletion. Limit
release-tag creation and release approval to maintainers.

## Signing key

Every release is signed twice: keyless with Sigstore (`checksums.txt.sigstore.json`,
used by `ctx update`) and with a minisign key (`checksums.txt.minisig`, used by
`install.sh`). The minisign signature covers `checksums.txt`, which lists the
SHA-256 of every archive, and its trusted comment is `ghosttree <tag>`, so the
signed list of one release cannot stand in for another.

- The public key is `deploy/downloads/ghosttree/minisign.pub` and the
  `pubkey='...'` line of `deploy/downloads/ghosttree/install.sh`. Until the key
  exists both hold a placeholder: `install.sh` refuses to run and the release
  workflow stops before it publishes.
- The secret key has no password (`minisign -G -W`). It lives in Bitwarden (vault
  dwl, "ghosttree release signing key (minisign)") and as the secret
  `MINISIGN_SECRET_KEY` of the GitHub **environment `release`** (not a repository
  secret), whose value is the complete key file. The environment only runs for
  `v*` tags, and only admins can create those (repository ruleset), so no branch,
  pull request or other workflow can read the key. The release job names the
  environment (`environment: release`).
- The workflow reads the secret in exactly one step, the one right before
  goreleaser. That step writes the key file, signs a probe and refuses to go on
  if the probe does not verify against `minisign.pub`. The step after goreleaser
  deletes the file, whatever the result. The actions that run earlier (Go, Node,
  Syft, Cosign) never see the key, and `actions/checkout` runs with
  `persist-credentials: false`, so the job token is not left in `.git/config`.
  `internal/installscript/workflow_test.go` checks all of this on every change to
  the workflow.
- Set up or rotate: generate a pair, update Bitwarden and the environment secret
  (TASK-159), then run
  `scripts/release-key.sh <new .pub file>` and merge the two changed files.
  Installers fetched before a rotation refuse releases signed with the new key
  and say so; people fetch the script again.
- Verify by hand: `minisign -Vm checksums.txt -p minisign.pub`, then
  `sha256sum -c checksums.txt --ignore-missing`.

### What the installer's checks do not cover

Say these next to the one-line install command, so nobody reads more into the
signature than it gives:

- **No protection against freezing.** `install.sh` asks GitHub's API (unsigned)
  which release is the newest. Someone who can cut that connection, or serve an
  old answer, keeps a machine on an older release that is still genuine and still
  signed: a minisign signature carries no date. Name the release you want with
  `--version vX.Y.Z` (compare it with the releases page) when that matters. The
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
