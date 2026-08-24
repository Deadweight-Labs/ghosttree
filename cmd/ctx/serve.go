package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/web"
)

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to the sqlite database")
	listen := fs.String("listen", "127.0.0.1:8474", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	// Open creates missing tables but never alters an existing one, so an
	// out-of-date knowledge table would only surface as puzzling SQL errors
	// once an agent writes. Refuse to serve instead.
	if current, err := store.SchemaCurrent(st.DB()); err != nil {
		fmt.Fprintf(stdout, "cannot inspect schema: %v\n", err)
		return 1
	} else if !current {
		fmt.Fprintf(stdout, "database schema is out of date - run 'ctx upgrade-schema --db %s' first\n", *db)
		return 1
	}
	if current, err := store.SchemaHasNewTypes(st.DB()); err != nil {
		fmt.Fprintf(stdout, "cannot inspect knowledge types: %v\n", err)
		return 1
	} else if !current {
		fmt.Fprintf(stdout, "database schema is out of date - run 'ctx upgrade-schema --db %s' first\n", *db)
		return 1
	}
	if _, err := st.ApplyStaleness(time.Now(), 90*24*time.Hour); err != nil {
		fmt.Fprintf(stdout, "apply knowledge staleness: %v\n", err)
		return 1
	}
	root := http.NewServeMux()
	root.Handle("/api/", server.New(st))
	root.Handle("/", web.New(st))
	fmt.Fprintf(stdout, "ghosttree %s listening on %s (db %s, ui /ui/)\n", version, *listen, *db)
	if err := http.ListenAndServe(*listen, root); err != nil {
		fmt.Fprintf(stdout, "serve: %v\n", err)
		return 1
	}
	return 0
}
