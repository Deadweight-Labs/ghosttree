# Contributing to Ghosttree

Thanks for taking the time to improve Ghosttree. A focused issue or small pull
request is the easiest place to start.

## Before you open a pull request

1. Search existing issues and requests for the same problem.
2. Open an issue before making a large behavioral or storage change.
3. Branch from `dev` using `feature/`, `fix/`, `docs/`, `test/`, or `chore/`.
4. Keep internal plans and specs out of the repository. Use Ghosttree's document
   lifecycle when the work needs a long-form design.
5. Add tests for changed behavior and run the local checks.

```bash
find cmd internal skills -name '*.go' -type f -print0 | xargs -0 gofmt -w
go vet ./...
go test -race -count=1 ./...
go mod tidy
git diff --exit-code -- go.mod go.sum
```

## Pull requests

Pull requests target `dev`; `main` only receives release and hotfix promotion.
Use Conventional Commit subjects such as `feat(documents): reject stale
revisions` or `fix(collector): resume partial uploads`.

The pull request template asks for a release impact:

- `major` for an incompatible change;
- `minor` for compatible functionality;
- `patch` for a compatible fix; or
- `none` for changes that do not affect a release.

Before 1.0, incompatible changes and new functionality normally bump the minor
version. Compatible fixes bump the patch version.

Every contribution must satisfy the [CLA](CLA.md). Checking the CLA declaration
in the pull request records acceptance; there is no bot that can accept it on
your behalf.

## Review expectations

A good pull request explains the user-visible problem, keeps unrelated changes
out, and includes concrete verification. Reviewers may ask for a smaller change
or an issue first when a proposal changes the storage model, security boundary,
license, or release process.

Please report vulnerabilities privately as described in
[SECURITY.md](SECURITY.md).
