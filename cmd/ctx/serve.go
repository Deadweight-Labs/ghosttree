package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/privatefile"
	"github.com/Deadweight-Labs/ghosttree/internal/proxytrust"
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
	Writer         store.WriterConfig
	OIDC           web.OIDCConfig
	PublicURL      string
	TrustedProxies proxytrust.Set
}

const (
	envOIDCIssuer       = "GHOSTTREE_OIDC_ISSUER"
	envOIDCClientID     = "GHOSTTREE_OIDC_CLIENT_ID"
	envOIDCClientSecret = "GHOSTTREE_OIDC_CLIENT_SECRET"
	envOIDCRedirectURL  = "GHOSTTREE_OIDC_REDIRECT_URL"
	// envEnforceAccess schaltet die Sichtbarkeit nach Rolle scharf. Ohne "1"
	// wird nur protokolliert, was verweigert würde ("access: would deny").
	envEnforceAccess = "GHOSTTREE_ENFORCE_ACCESS"
	// envPublicURL and envTrustedProxies describe the TLS/proxy boundary; see
	// the README section "Running behind TLS or a reverse proxy".
	envPublicURL      = "GHOSTTREE_PUBLIC_URL"
	envTrustedProxies = "GHOSTTREE_TRUSTED_PROXIES"
)

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.OpenRuntime(cfg.DB, cfg.Writer)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return 1
	}
	defer st.Close()
	code := runServer(ctx, st, cfg, stdout, os.Stderr)
	if err := st.Close(); err != nil {
		fmt.Fprintf(stdout, "close store: %v\n", err)
		return 1
	}
	return code
}

func parseServeConfig(args []string, output io.Writer) (serveConfig, error) {
	limits := snapshot.DefaultLimits()
	cfg := serveConfig{SnapshotLimits: limits, Writer: store.DefaultWriterConfig()}
	var roots snapshotRootValues
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&cfg.DB, "db", "ghosttree.db", "path to the sqlite database")
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8474", "listen address")
	fs.IntVar(&cfg.Writer.MaxOperations, "writer-max-operations", cfg.Writer.MaxOperations, "maximum accepted unfinished writer operations")
	fs.Int64Var(&cfg.Writer.MaxBytes, "writer-max-bytes", cfg.Writer.MaxBytes, "maximum reserved writer payload bytes including active work")
	fs.IntVar(&cfg.Writer.MaxBatch, "writer-max-batch", cfg.Writer.MaxBatch, "maximum contiguous chunk operations per commit")
	fs.IntVar(&cfg.Writer.ReadConnections, "writer-read-connections", cfg.Writer.ReadConnections, "read-only pool connection limit")
	fs.Int64Var(&cfg.SnapshotLimits.MaxEntryPayloadBytes, "snapshot-max-entry-bytes", limits.MaxEntryPayloadBytes, "maximum payload bytes per snapshot entry")
	fs.Int64Var(&cfg.SnapshotLimits.MaxEntriesPerSnapshot, "snapshot-max-entries", limits.MaxEntriesPerSnapshot, "maximum entries per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotPayloadBytes, "snapshot-max-payload-bytes", limits.MaxSnapshotPayloadBytes, "maximum payload bytes per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxCanonicalHeadBytes, "snapshot-max-head-bytes", limits.MaxCanonicalHeadBytes, "maximum canonical head bytes per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotLogicalBytes, "snapshot-max-logical-bytes", limits.MaxSnapshotLogicalBytes, "maximum logical bytes per snapshot")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotsPerProject, "snapshot-max-project-count", limits.MaxSnapshotsPerProject, "maximum snapshots per project")
	fs.Int64Var(&cfg.SnapshotLimits.MaxProjectLogicalBytes, "snapshot-max-project-bytes", limits.MaxProjectLogicalBytes, "maximum logical snapshot bytes per project")
	fs.Int64Var(&cfg.SnapshotLimits.MaxSnapshotsPerStore, "snapshot-max-store-count", limits.MaxSnapshotsPerStore, "maximum snapshots in the store")
	fs.Int64Var(&cfg.SnapshotLimits.MaxStoreLogicalBytes, "snapshot-max-store-bytes", limits.MaxStoreLogicalBytes, "maximum logical snapshot bytes in the store")
	fs.StringVar(&cfg.OIDC.Issuer, "oidc-issuer", os.Getenv(envOIDCIssuer), "OIDC issuer URL, e.g. https://id.example.com (env "+envOIDCIssuer+")")
	fs.StringVar(&cfg.OIDC.ClientID, "oidc-client-id", os.Getenv(envOIDCClientID), "OIDC client id (env "+envOIDCClientID+")")
	fs.StringVar(&cfg.OIDC.RedirectURL, "oidc-redirect-url", os.Getenv(envOIDCRedirectURL), "OIDC redirect URL, https://<public host>/ui/login/oidc/callback (env "+envOIDCRedirectURL+")")
	fs.StringVar(&cfg.PublicURL, "public-url", os.Getenv(envPublicURL), "external base URL, e.g. https://ghosttree.example.com; an https URL makes every cookie Secure (env "+envPublicURL+")")
	trusted := fs.String("trusted-proxies", os.Getenv(envTrustedProxies), "comma-separated CIDRs/IPs of reverse proxies whose X-Forwarded-Proto/Host are believed; loopback is always trusted, nothing else by default (env "+envTrustedProxies+")")
	// Das Secret gibt es bewusst nur über die Umgebung: ein Flag stünde in der Prozessliste.
	cfg.OIDC.ClientSecret = os.Getenv(envOIDCClientSecret)
	fs.Var(&roots, "snapshot-root", "project mirror root as PROJECT=ABSOLUTE_PATH; repeatable")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	if fs.NArg() != 0 {
		return serveConfig{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := cfg.OIDC.Validate(); err != nil {
		return serveConfig{}, err
	}
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return serveConfig{}, fmt.Errorf("--public-url must be http(s)://host[:port] without path, got %q", cfg.PublicURL)
		}
		cfg.PublicURL = u.Scheme + "://" + u.Host
	}
	proxies, err := proxytrust.Parse(*trusted)
	if err != nil {
		return serveConfig{}, fmt.Errorf("--trusted-proxies: %w", err)
	}
	cfg.TrustedProxies = proxies
	if err := validateSnapshotLimits(cfg.SnapshotLimits); err != nil {
		return serveConfig{}, err
	}
	if cfg.Writer.MaxOperations <= 0 || cfg.Writer.MaxBytes <= 0 || cfg.Writer.MaxBatch <= 0 || cfg.Writer.ReadConnections <= 0 {
		return serveConfig{}, fmt.Errorf("writer limits must be positive finite values")
	}
	if cfg.Writer.MaxBytes < cfg.SnapshotLimits.MaxSnapshotLogicalBytes {
		return serveConfig{}, fmt.Errorf("--writer-max-bytes must be at least --snapshot-max-logical-bytes")
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

func runServer(ctx context.Context, st *store.Store, cfg serveConfig, stdout, stderr io.Writer) int {
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
	if err := prepareBootstrapCode(st, cfg.DB, stdout); err != nil {
		fmt.Fprintf(stdout, "bootstrap code: %v\n", err)
		return 1
	}
	if ctx.Err() != nil {
		return 0
	}
	fmt.Fprintf(stdout, "ghosttree %s listening on %s (db %s, ui /ui/)\n", version, cfg.Listen, cfg.DB)
	slog.New(slog.NewJSONHandler(stderr, nil)).Info("access_mode", "enforce", accessEnforcedByEnv(), "env", envEnforceAccess,
		"note", "without enforcement only 'access: would deny' is logged, nothing is refused")
	slog.New(slog.NewJSONHandler(stderr, nil)).Info("writer_config", "max_operations", cfg.Writer.MaxOperations, "max_bytes", cfg.Writer.MaxBytes, "max_batch", cfg.Writer.MaxBatch, "read_connections", cfg.Writer.ReadConnections)
	if err := serveUntilCanceled(ctx, newHTTPServer(cfg.Listen, buildServerHandler(st, cfg, stderr))); err != nil {
		fmt.Fprintf(stdout, "serve: %v\n", err)
		return 1
	}
	return 0
}

func accessEnforcedByEnv() bool { return os.Getenv(envEnforceAccess) == "1" }

func buildServerHandler(st *store.Store, cfg serveConfig, stderr io.Writer) http.Handler {
	root := http.NewServeMux()
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	options := []server.Option{
		server.WithContextSnapshotLimits(cfg.SnapshotLimits),
		server.WithLogger(logger),
		server.WithBuildVersion(version),
		server.WithTrustedProxies(cfg.TrustedProxies),
	}
	if cfg.PublicURL != "" {
		options = append(options, server.WithPublicURL(cfg.PublicURL))
	}
	if len(cfg.SnapshotRoots) > 0 {
		options = append(options, server.WithSnapshotMirror(&rootedSnapshotMirror{source: st, roots: cfg.SnapshotRoots}))
	}
	st.SetAccessMode(store.AccessMode{Enforce: accessEnforcedByEnv(), Logger: logger})
	apiHandler := server.New(st, options...)
	root.Handle("/api/", apiHandler)
	root.Handle("/metrics", apiHandler)
	webOptions := []web.Option{web.WithBootstrapFile(bootstrapCodePath(cfg.DB)), web.WithTrustedProxies(cfg.TrustedProxies)}
	if cfg.PublicURL != "" {
		webOptions = append(webOptions, web.WithPublicURL(cfg.PublicURL))
	}
	if cfg.OIDC.Enabled() {
		webOptions = append(webOptions, web.WithOIDC(cfg.OIDC))
	}
	root.Handle("/", web.New(st, webOptions...))
	return root
}

func bootstrapCodePath(db string) string {
	return filepath.Join(filepath.Dir(db), "bootstrap-code")
}

// prepareBootstrapCode legt auf einer leeren Instanz einen Bootstrap-Code an
// und schreibt ihn in <Datenverzeichnis>/bootstrap-code (0600). Der Pfad wird
// einmal gemeldet, der Code nie. Auf einer Instanz mit Konten bleibt keine
// alte Datei liegen.
func prepareBootstrapCode(st *store.Store, db string, stdout io.Writer) error {
	path := bootstrapCodePath(db)
	code, ok, err := st.EnsureBootstrapCode()
	if err != nil {
		return err
	}
	if !ok {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := privatefile.Write(path, []byte(code+"\n")); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "empty instance: one-time bootstrap code written to %s (valid %s); present it on the sign-in page to create the first account\n", path, store.BootstrapCodeTTL)
	return nil
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
