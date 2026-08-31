package privatefile

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

type fileOperations struct {
	write   func(*os.File, []byte) error
	sync    func(*os.File) error
	close   func(*os.File) error
	replace func(string, string) error
	dirSync func(string) error
}

var defaultFileOps = fileOperations{
	write: func(f *os.File, data []byte) error {
		for len(data) > 0 {
			n, err := f.Write(data)
			if err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	},
	sync:    (*os.File).Sync,
	close:   (*os.File).Close,
	replace: atomicReplace,
	dirSync: syncDirectory,
}

var (
	writeOpsMu sync.Mutex
	writeOps   = defaultFileOps
)

// WriteSyncedNoFollow writes data through a private inode in the destination
// directory, flushes it, atomically replaces the destination, and flushes the
// directory entry. It resolves the existing destination directory once and
// rejects a symlink at the final filename. Callers must prevent concurrent
// namespace mutation; the path-based portable implementation is not a
// dirfd/handle-relative defense against an active rename race.
func WriteSyncedNoFollow(path string, data []byte, mode fs.FileMode) (err error) {
	writeOpsMu.Lock()
	ops := writeOps
	writeOpsMu.Unlock()
	if mode&^fs.FileMode(0o777) != 0 {
		return fmt.Errorf("privatefile: invalid mode %v", mode)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	path = filepath.Join(dir, filepath.Base(path))

	var old []byte
	var oldMode fs.FileMode
	hadOld := false
	if existing, openErr := openNoFollow(path); openErr == nil {
		info, statErr := existing.Stat()
		if statErr != nil {
			_ = existing.Close()
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_ = existing.Close()
			return fmt.Errorf("privatefile: destination is a symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			_ = existing.Close()
			return fmt.Errorf("privatefile: destination is not a regular file: %s", path)
		}
		old, err = io.ReadAll(existing)
		closeErr := existing.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		oldMode, hadOld = info.Mode().Perm(), true
	} else if !os.IsNotExist(openErr) {
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("privatefile: destination is a symlink: %s", path)
		}
		return openErr
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ops.write(tmp, data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ops.sync(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ops.close(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ops.replace(tmpPath, path); err != nil {
		return err
	}
	keep = true
	if err := ops.dirSync(dir); err != nil {
		if restoreErr := restore(path, old, oldMode, hadOld); restoreErr != nil {
			return fmt.Errorf("sync destination directory: %w (restore failed: %v)", err, restoreErr)
		}
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}

func restore(path string, old []byte, oldMode fs.FileMode, hadOld bool) error {
	if !hadOld {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(oldMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(old); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := atomicReplace(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
