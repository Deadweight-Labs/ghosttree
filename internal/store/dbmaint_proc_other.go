//go:build !linux

package store

// Ohne /proc lässt sich nicht nachsehen, wer die Datei offen hat; übrig bleibt
// die Schreibsperre und der Checkpoint in VacuumDatabase.
func scanDatabaseHolders(string) HolderScan { return HolderScan{} }
