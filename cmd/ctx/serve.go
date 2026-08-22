package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
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
	fmt.Fprintf(stdout, "ghosttree %s listening on %s (db %s)\n", version, *listen, *db)
	if err := http.ListenAndServe(*listen, server.New(st)); err != nil {
		fmt.Fprintf(stdout, "serve: %v\n", err)
		return 1
	}
	return 0
}
