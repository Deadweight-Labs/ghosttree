package selfupdate

import (
	"errors"
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
)

// MinisignVerifier checks signatures against the key key returns (the release
// key embedded in this ctx). A key that cannot be had, such as the placeholder
// in a build made before the key existed, turns into ErrCannotCheck.
func MinisignVerifier(key func() (minisign.PublicKey, error)) Verifier {
	return func(sums, sig []byte) (string, error) {
		pk, err := key()
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrCannotCheck, err)
		}
		trusted, err := minisign.Verify(pk, sums, sig)
		if err != nil {
			return "", errors.New(err.Error() + " (the checksums are not the ones the ghosttree release key signed)")
		}
		return trusted, nil
	}
}
