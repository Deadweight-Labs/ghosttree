package main

import (
	"os"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/collector"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/gitanchor"
)

// anchoredClient liefert den Client für ctx-Befehle, die in den Ledger
// schreiben (REQ-597): jeder Schreibzugriff trägt den Git-Stand des
// Arbeitsverzeichnisses mit, aus dem der Befehl läuft, und die Session, die ihn
// aufruft (REQ-600). Das Verzeichnis wird je Schreibzugriff gelesen, nicht beim
// Start; ohne Git ist der Anker leer und der Befehl läuft ohne ihn.
//
// Nur für Befehle, die im Arbeitsverzeichnis des Agenten laufen. Verwaltungs-
// befehle und Hooks nehmen client.New: dort wäre das Verzeichnis zufällig.
func anchoredClient(cfg config.Config) *client.Client {
	c := client.New(cfg)
	c.SetAnchorSource(func() gitanchor.Anchor {
		cwd, err := os.Getwd()
		if err != nil {
			return gitanchor.Anchor{}
		}
		return collector.ResolveAnchor(cwd)
	})
	c.SetOriginSession(currentSessionRef())
	return c
}
