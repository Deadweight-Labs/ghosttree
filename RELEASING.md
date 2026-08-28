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

## Final release

1. Confirm the chosen release-candidate commit is green and approved.
2. Merge `release/X.Y.Z` into `main` by pull request.
3. Create the annotated tag `vX.Y.Z` on the resulting `main` commit.
4. Push the tag and verify archives, checksums, SBOMs, and signatures.
5. Merge `main` back into `dev` and remove the release branch.

Stable release artifacts are built only from final tags. A failed publication
is corrected with a new tag and version.

## Required repository settings

GitHub branch protection is part of the release boundary and must be configured
for both `dev` and `main`: pull requests, required reviews, required status
checks, resolved conversations, no force pushes, and no branch deletion. Limit
release-tag creation and release approval to maintainers.
