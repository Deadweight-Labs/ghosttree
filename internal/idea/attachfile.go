package idea

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A path that comes from an agent (or from a prompt an agent read) must not make
// the client send any file of the machine to the server: a key, a token file, a
// browser profile. Only a regular file that really holds a PNG, JPEG or WebP
// goes out.

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}

// ReadImageFile reads an image file for an attachment: a regular file (not a
// pipe or device; a link only to such a file), at most MaxAttachmentBytes, whose
// content is an image by its first bytes.
func ReadImageFile(path string) ([]byte, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("%q is not a file path", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symbolic link; name the image itself", path)
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > MaxAttachmentBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxAttachmentBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxAttachmentBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, MaxAttachmentBytes)
	}
	if _, err := CheckImage(data, ""); err != nil {
		return nil, fmt.Errorf("%s is not a PNG, JPEG or WebP image: %w", path, err)
	}
	return data, nil
}

// ReadImageFileStrict is ReadImageFile for a path an agent names: the name (and
// the file a link leads to) has an image extension, and no part of the path
// below the home directory starts with a dot (.ssh, .config, .aws, .gnupg).
func ReadImageFileStrict(path string) ([]byte, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("%q is not a file path", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{abs, resolved} {
		if !imageExts[strings.ToLower(filepath.Ext(p))] {
			return nil, fmt.Errorf("%s does not have an image extension (.png, .jpg, .jpeg, .webp)", p)
		}
		if part, ok := hiddenUnderHome(p); ok {
			return nil, fmt.Errorf("%s is inside %s, a hidden folder of the home directory; copy the image elsewhere or send it as data_base64", p, part)
		}
	}
	return ReadImageFile(resolved)
}

// hiddenUnderHome reports the first part of p, below the home directory, that
// starts with a dot.
func hiddenUnderHome(p string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	for _, h := range homeForms(home) {
		rel, err := filepath.Rel(h, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if strings.HasPrefix(part, ".") && part != "." {
				return part, true
			}
		}
	}
	return "", false
}

// homeForms is the home directory as named and as it resolves, since a path may
// use either.
func homeForms(home string) []string {
	forms := []string{filepath.Clean(home)}
	if r, err := filepath.EvalSymlinks(home); err == nil && r != forms[0] {
		forms = append(forms, r)
	}
	return forms
}
