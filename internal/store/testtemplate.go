package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A test binary opens thousands of fresh stores, and every one of them replays
// the full schema and all migrations: about 100 ms normally, 3.3 s under the
// race detector, which put the web package alone past 70 minutes. Inside a test
// binary (testing.Testing) the first fresh open migrates once, keeps the closed
// database file in memory, and every later open of a path that does not exist
// yet starts from a copy of it. The copy is exactly what the migrations would
// have produced; Open still runs all of its idempotent steps on it, so a store
// seeded from the template is indistinguishable from a freshly migrated one
// (TestSeededStoreMatchesFreshMigration holds that). A path that already
// exists, such as a legacy database a test built by hand, is never touched.
// GHOSTTREE_TEST_FRESH_STORE=1 turns the seeding off.
var testTemplate struct {
	once sync.Once
	data []byte
}

// testMemoryTemplate is the same for ":memory:" stores: the serialized image of
// a migrated in-memory database, loaded into the one connection of a new one.
var testMemoryTemplate struct {
	once sync.Once
	data []byte
}

// sqliteImage is what the driver's connection offers to read and load a whole
// database as bytes; it is not part of database/sql, so it is asserted.
type sqliteImage interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// seedFromTestTemplate writes the migrated template to path when this is a test
// binary and path is a database that does not exist yet. Any failure leaves
// path alone, and the caller migrates from scratch as it always did.
func seedFromTestTemplate(path string) {
	if !testing.Testing() || os.Getenv("GHOSTTREE_TEST_FRESH_STORE") == "1" {
		return
	}
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return
	}
	testTemplate.once.Do(buildTestTemplate)
	if len(testTemplate.data) == 0 {
		return
	}
	// O_EXCL: if another opener creates the file first, it wins and we stay out.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, werr := f.Write(testTemplate.data)
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(path)
	}
}

func buildTestTemplate() {
	dir, err := os.MkdirTemp("", "ghosttree-store-template-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	st, err := openWithOptions(path, OpenOptions{MaxOpenConns: 1}, false)
	if err != nil {
		return
	}
	if err := st.Close(); err != nil {
		return
	}
	// Only a database SQLite has folded back into one file is a usable copy.
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Size() > 0 {
			return
		}
	}
	if data, err := os.ReadFile(path); err == nil {
		testTemplate.data = data
	}
}

// seedMemoryFromTestTemplate loads the migrated in-memory template into the
// still empty private ":memory:" database behind db. Any failure leaves the
// database empty, and the caller migrates from scratch as it always did.
func seedMemoryFromTestTemplate(db *sql.DB) {
	if !testing.Testing() || os.Getenv("GHOSTTREE_TEST_FRESH_STORE") == "1" {
		return
	}
	testMemoryTemplate.once.Do(buildTestMemoryTemplate)
	if len(testMemoryTemplate.data) == 0 {
		return
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.Raw(func(driverConn any) error {
		if img, ok := driverConn.(sqliteImage); ok {
			return img.Deserialize(testMemoryTemplate.data)
		}
		return nil
	})
}

func buildTestMemoryTemplate() {
	st, err := openWithOptions(":memory:", OpenOptions{MaxOpenConns: 1}, false)
	if err != nil {
		return
	}
	defer st.Close()
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.Raw(func(driverConn any) error {
		img, ok := driverConn.(sqliteImage)
		if !ok {
			return nil
		}
		data, err := img.Serialize()
		if err == nil {
			testMemoryTemplate.data = data
		}
		return err
	})
}
