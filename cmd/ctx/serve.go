package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/scope"
	"github.com/Deadweight-Labs/ghosttree/internal/server"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshot"
	"github.com/Deadweight-Labs/ghosttree/internal/snapshotmirror"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
	"github.com/Deadweight-Labs/ghosttree/internal/web"
)

type serveConfig struct {
	DB             string
	Listen         string
	SnapshotLimits snapshot.Limits
	SnapshotRoots  map[string]string
}

type snapshotRootValues []string

func (v *snapshotRootValues) String() string { return strings.Join(*v, ",") }
func (v *snapshotRootValues) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func cmdServe(args []string, stdout io.Writer) int {
	cfg, err := parseServeConfig(args, stdout)
	if err != nil {
		fmt.Fprintf(stdout, "serve configuration: %v\n", err)
		return 2
	}
	st, err := store.Open(cfg.DB)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	return runServer(st, cfg, stdout)
}

func parseServeConfig(args []string, output io.Writer) (serveConfig, error) {
	limits := snapshot.DefaultLimits()
	cfg := serveConfig{SnapshotLimits: limits}
	var roots snapshotRootValues
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.DB, "db", "ghosttree.db", "path to the sqlite database")
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8474", "listen address")
	fs.Int64Var(&cfg.SnapshotLimits.MaxEntryPayloadBytes, "snapshot-max-entry-bytes", limits.MaxEntryPayloadBytes, "maximum payload bytes per snapshot entry")
	fs.Int64Var(&cfg.SnapshotLimits.MaxEntriesPerSnapshot, "snapshot-max-entries", limits.MaxEntriesPerSnapshot, "maximum entries per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotPayloadBytes, "snapshot-max-payload-bytes", limits.MaxSnapshotPayloadBytes, "maximum payload bytes per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxCanonicalHeadBytes, "snapshot-max-head-bytes", limits.MaxCanonicalHeadBytes, "maximum canonical head bytes per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotLogicalBytes, "snapshot-max-logical-bytes", limits.MaxSnapshotLogicalBytes, "maximum logical bytes per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotsPerProject, "snapshot-max-project-count", limits.MaxSnapshotsPerProject, "maximum snapshots per project")
	fs.Int64Var(&cfg.SnapshotLimits.MaxProjectLogicalBytes, "snapshot-max-project-bytes", limits.MaxProjectLogicalBytes, "maximum logical snapshot bytes per project")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotsPerStore, "snapshot-max-store-count", limits.MaxSnapshotsPerStore, "maximum snapshots in the store")
	fs.Int64Var(&cfg.SnapshotLimits.MaxStoreLogicalBytes, "snapshot-max-store-bytes", limits.MaxStoreLogicalBytes, "maximum logical snapshot bytes in the store")
	fs.Var(&roots, "snapshot-root", "project mirror root as PROJECT=ABSOLUTE_PATH; repeatable")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	if fs.NArg() != 0 {
		return serveConfig{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateSnapshotLimits(cfg.SnapshotLimits); err != nil {
		return serveConfig{}, err
	}
	parsedRoots, err := parseSnapshotRoots(roots)
	if err != nil {
		return serveConfig{}, err
	}
	cfg.SnapshotRoots = parsedRoots
	return cfg, nil
}

func validateSnapshotLimits(limits snapshot.Limits) error {
	values := []struct {
		name  string
		value int64
	}{
		{"snapshot-max-entry-bytes", limits.MaxEntryPayloadBytes},
		{"snapshot-max-entries", limits.MaxEntriesPerSnapshot},
		{"snapshot-max-payload-bytes", limits.MaxSnapshotPayloadBytes},
		{"snapshot-max-head-bytes", limits.MaxCanonicalHeadBytes},
		{"snapshot-max-logical-bytes", limits.MaxSnapshotLogicalBytes},
		{"snapshot-max-project-count", limits.MaxSnapshotsPerProject},
		{"snapshot-max-project-bytes", limits.MaxProjectLogicalBytes},
		{"snapshot-max-store-count", limits.MaxSnapshotsPerStore},
		{"snapshot-max-store-bytes", limits.MaxStoreLogicalBytes},
	}
	for _, value := range values {
		if value.value <= 0 {
			return fmt.Errorf("--%s must be a positive finite value", value.name)
		}
	}
	return nil
}

func parseSnapshotRoots(values []string) (map[string]string, error) {
	roots := make(map[string]string, len(values))
	for _, value := range values {
		project, root, ok := strings.Cut(value, "=")
		project = scope.NormalizeRemote(project)
		if !ok || project == "" || root == "" {
			return nil, fmt.Errorf("--snapshot-root must be PROJECT=ABSOLUTE_PATH")
		}
		if _, duplicate := roots[project]; duplicate {
			return nil, fmt.Errorf("duplicate --snapshot-root project %q", project)
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("snapshot root for %q must be absolute", project)
		}
		root = filepath.Clean(root)
		info, err := os.Lstat(root)
		if err != nil {
			return nil, fmt.Errorf("snapshot root for %q: %w", project, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("snapshot root for %q must be a real directory", project)
		}
		roots[project] = root
	}
	return roots, nil
}

func runServer(st *store.Store, cfg serveConfig, stdout io.Writer) int {
	// Open creates missing tables but never alters an existing one, so an
	// out-of-date knowledge table would only surface as puzzling SQL errors
	// once an agent writes. Refuse to serve instead.
	if current, err := store.SchemaCurrent(st.DB()); err != nil {
		fmt.Fprintf(stdout, "cannot inspect schema: %v\n", err)
		return 1
	} else if !current {
		fmt.Fprintf(stdout, "database schema is out of date - run 'ctx upgrade-schema --db %s' first\n", cfg.DB)
		return 1
	}
	if current, err := store.SchemaHasNewTypes(st.DB()); err != nil {
		fmt.Fprintf(stdout, "cannot inspect knowledge types: %v\n", err)
		return 1
	} else if !current {
		fmt.Fprintf(stdout, "database schema is out of date - run 'ctx upgrade-schema --db %s' first\n", cfg.DB)
		return 1
	}
	if err := serveSnapshotSchemaReady(context.Background(), st.DB()); err != nil {
		fmt.Fprintf(stdout, "context snapshot schema is not ready: %v\n", err)
		return 1
	}
	if _, err := st.ApplyStaleness(time.Now(), 90*24*time.Hour); err != nil {
		fmt.Fprintf(stdout, "apply knowledge staleness: %v\n", err)
		return 1
	}
	root := http.NewServeMux()
	options := []server.Option{server.WithContextSnapshotLimits(cfg.SnapshotLimits)}
	if len(cfg.SnapshotRoots) > 0 {
		options = append(options, server.WithSnapshotMirror(&rootedSnapshotMirror{source: st, roots: cfg.SnapshotRoots}))
	}
	root.Handle("/api/", server.New(st, options...))
	root.Handle("/", web.New(st))
	fmt.Fprintf(stdout, "ghosttree %s listening on %s (db %s, ui /ui/)\n", version, cfg.Listen, cfg.DB)
	if err := newHTTPServer(cfg.Listen, root).ListenAndServe(); err != nil {
		fmt.Fprintf(stdout, "serve: %v\n", err)
		return 1
	}
	return 0
}

type rootedSnapshotMirror struct {
	source any
	roots  map[string]string
}

func (m *rootedSnapshotMirror) Rebuild(ctx context.Context, project string) error {
	root, ok := m.roots[scope.NormalizeRemote(project)]
	if !ok {
		return nil
	}
	lister, ok := m.source.(snapshotmirror.SnapshotLister)
	if !ok {
		return fmt.Errorf("snapshot store does not support listing snapshots")
	}
	return snapshotmirror.Rebuild(ctx, lister, root, scope.NormalizeRemote(project))
}

func serveSnapshotSchemaReady(ctx context.Context, db *sql.DB) error {
	current, err := store.ContextSnapshotSchemaCurrent(db)
	if err != nil {
		return err
	}
	if !current {
		return fmt.Errorf("database schema is out of date")
	}
	return store.ProbeContextSnapshotSchema(ctx, db)
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
}
