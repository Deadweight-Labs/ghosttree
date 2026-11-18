package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxSecretFileBytes = 4096

// oidcClientSecret is the client secret of the identity provider: the value of
// GHOSTTREE_OIDC_CLIENT_SECRET, or the one line of the file named by
// GHOSTTREE_OIDC_CLIENT_SECRET_FILE, never both. A file other users can read is
// refused, as a key file would be.
func oidcClientSecret(fromEnv, file string) (string, error) {
	if file == "" {
		return fromEnv, nil
	}
	if fromEnv != "" {
		return "", fmt.Errorf("oidc: set only one of %s and %s", envOIDCClientSecret, envOIDCClientSecretFile)
	}
	fi, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("oidc: %s: %w", envOIDCClientSecretFile, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("oidc: %s %s is not a regular file", envOIDCClientSecretFile, file)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		return "", fmt.Errorf("oidc: %s is readable by other users; make it %s", file, "0400 or 0640")
	}
	if fi.Size() > maxSecretFileBytes {
		return "", fmt.Errorf("oidc: %s is too large for a secret", file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("oidc: %s: %w", envOIDCClientSecretFile, err)
	}
	secret := strings.TrimRight(string(b), "\r\n")
	switch {
	case secret == "":
		return "", errors.New("oidc: the file in " + envOIDCClientSecretFile + " is empty")
	case strings.ContainsAny(secret, "\r\n"):
		return "", errors.New("oidc: the file in " + envOIDCClientSecretFile + " must hold one line")
	}
	return secret, nil
}
