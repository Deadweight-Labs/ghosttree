package runbook

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Runner führt eine argv-Liste aus. Die Umsetzung ruft nie eine Shell auf.
type Runner interface {
	Run(ctx context.Context, argv []string) error
}

// OSRunner startet den Prozess im Vordergrund des aufrufenden Prozesses mit
// dessen Ein- und Ausgabe. argv[0] wird über den Suchpfad aufgelöst und
// unverändert als Programm gestartet; die übrigen Elemente sind genau die
// Argumente, Zeichen für Zeichen.
type OSRunner struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

func (r OSRunner) Run(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.Stdin, r.Stdout, r.Stderr
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	return cmd.Run()
}

// Execute führt die Schritte der Reihe nach aus und hält beim ersten Fehler an.
// Es liefert die Zahl der erfolgreich beendeten Schritte und den Fehler.
func Execute(ctx context.Context, r Runner, plan []Exec, progress io.Writer) (int, error) {
	for i, st := range plan {
		if progress != nil {
			fmt.Fprintf(progress, "[%d/%d] $ %s\n", i+1, len(plan), QuoteArgv(st.Argv))
		}
		if err := r.Run(ctx, st.Argv); err != nil {
			return i, fmt.Errorf("step %d failed: %w", i+1, err)
		}
	}
	return len(plan), nil
}
