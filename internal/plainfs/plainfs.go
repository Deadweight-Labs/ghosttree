// Package plainfs reads and writes the files of a local worktree that people
// and agents edit (.ghosttree/edit/ for documents, .ghosttree/workflows/ for
// workflows) without ever following a symbolic link.
//
// A worktree directory comes with a checkout, so a repository can bring
// something there: a link (deploy.md -> ~/.bash_aliases) would make a pull
// write server text outside the worktree. Every directory on the way has to be
// a plain directory, every file a regular one; files are opened without
// following a link and written through a new temporary file and a rename,
// which replaces a directory entry and never writes through one.
package plainfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ErrNotPlain refuses what is not a plain directory or a regular file.
func ErrNotPlain(path string) error {
	return fmt.Errorf("%s is not a plain directory or regular file (a symbolic link?); ghosttree does not follow it", path)
}

// CheckDirs refuses a directory in the list that exists and is not a plain
// directory. List them from the outside in; a missing one ends the check, as
// nothing below it can exist.
func CheckDirs(dirs ...string) error {
	for _, dir := range dirs {
		fi, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsDir() {
			return ErrNotPlain(dir)
		}
	}
	return nil
}

// Read reads a regular file without following a symbolic link.
func Read(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrNotPlain(path)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotPlain(path)
	}
	return io.ReadAll(f)
}

// Write writes a file through a new temporary file beside it and a rename.
// dirs are the directories from the outside in that have to be plain, the
// last one being the directory of the file; missing ones are made. An
// existing entry that is not a regular file is refused.
func Write(path string, data []byte, dirs ...string) error {
	if err := CheckDirs(dirs...); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := CheckDirs(dirs...); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return ErrNotPlain(path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
