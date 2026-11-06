// Command ledgerly runs the billing API.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/acme-billing/ledgerly/internal/api"
	"github.com/acme-billing/ledgerly/internal/migrate"
	"github.com/acme-billing/ledgerly/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate-check" {
		migs, err := migrate.Discover("migrations")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, m := range migs {
			fmt.Printf("%04d %s (down: %v)\n", m.Version, m.Name, m.Down != "")
		}
		return
	}
	addr := flag.String("listen", "127.0.0.1:8080", "listen address")
	seed := flag.Bool("seed", true, "load demo data")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	st := store.New()
	if *seed {
		store.Seed(st)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewServer(st, api.WithLogger(logger)).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("listening", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
