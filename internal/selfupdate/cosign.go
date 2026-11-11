package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// OIDCIssuer is the issuer of the GitHub Actions identity that signs releases.
const OIDCIssuer = "https://token.actions.githubusercontent.com"

// CosignVerifier checks the keyless Sigstore bundle release.yml produces
// (cosign sign-blob --bundle checksums.txt.sigstore.json) by running
// `cosign verify-blob`, pinned to the release workflow of repo ("owner/name")
// at exactly the tag being installed. cosignPath may be empty (found on PATH).
// Without cosign the answer is an error: ctx has no Sigstore library of its
// own, and an unverified release is never installed.
func CosignVerifier(repo, cosignPath string) Verifier {
	return func(ctx context.Context, checksumsFile, bundleFile, tag string) error {
		path := cosignPath
		if path == "" {
			p, err := exec.LookPath("cosign")
			if err != nil {
				return errors.New("cosign not found: the stable channel verifies the Sigstore signature with it " +
					"(install cosign, or use --channel dev / --source); nothing was changed")
			}
			path = p
		}
		identity := "(?i)^" + regexp.QuoteMeta("https://github.com/"+repo+"/.github/workflows/release.yml@refs/tags/"+tag) + "$"
		cmd := exec.CommandContext(ctx, path, "verify-blob",
			"--bundle", bundleFile,
			"--certificate-identity-regexp", identity,
			"--certificate-oidc-issuer", OIDCIssuer,
			checksumsFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cosign verify-blob: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}
