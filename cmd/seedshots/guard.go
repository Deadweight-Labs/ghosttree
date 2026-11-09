package main

import (
	"fmt"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// requireEmptyDatabase verweigert eine Datenbank, in der es schon Personen
// gibt: seedshots legt feste Konten mit Anmeldecodes an und gehört nur in eine
// frische Wegwerf-Datenbank, nie in eine, die jemand benutzt.
func requireEmptyDatabase(st *store.Store, path string) error {
	people, err := st.ListAccounts()
	if err != nil {
		return fmt.Errorf("seedshots: cannot read %s: %w", path, err)
	}
	if len(people) > 0 {
		return fmt.Errorf("seedshots: %s already holds %d account(s); refusing to seed a database in use. Pass the path of a new, empty database", path, len(people))
	}
	return nil
}
