package web

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed install.sh
var installScript string

const installServerPlaceholder = "__GHOSTTREE_SERVER__"

// WithDistDir points the server at a directory holding the ctx release
// archives (ctx_<version>_<os>_<arch>.tar.gz) and their checksums.txt, in the
// layout goreleaser writes. Without it /install.sh and /dist/ answer 404.
func WithDistDir(dir string) Option { return func(a *app) { a.distDir = dir } }

var distNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

var (
	distHashRE    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	distVersionRE = regexp.MustCompile(`^ctx_(.+)_(?:linux|darwin)_(?:amd64|arm64)\.tar\.gz$`)
)

// parseChecksums reads sha256sum/goreleaser lines "<64 hex>  <name>" (a "*"
// before the name, CR line ends and anything malformed are tolerated or
// dropped; install.sh applies the same rules). A name with a path separator or
// anything outside a plain file name is dropped, so the list can never point
// out of the directory.
func parseChecksums(r *bufio.Scanner) map[string]bool {
	names := map[string]bool{"checksums.txt": true}
	for r.Scan() {
		fields := strings.Fields(strings.TrimRight(r.Text(), "\r"))
		if len(fields) != 2 || !distHashRE.MatchString(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if distNameRE.MatchString(name) && !strings.Contains(name, "..") {
			names[name] = true
		}
	}
	return names
}

// openDistFile opens a regular, non-symlink file of dir. The Lstat/Fstat
// comparison closes the window between the check and the open.
func openDistFile(dir, name string) (*os.File, os.FileInfo, error) {
	path := filepath.Join(dir, name)
	li, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !li.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !os.SameFile(li, fi) {
		f.Close()
		return nil, nil, errors.New("file changed")
	}
	return f, fi, nil
}

// CheckDistDir validates a --dist-dir and returns the distinct ctx versions
// its checksums.txt lists (more than one makes install.sh refuse, so the
// caller should warn).
func CheckDistDir(dir string) ([]string, error) {
	f, _, err := openDistFile(dir, "checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("checksums.txt: %w", err)
	}
	defer f.Close()
	seen := map[string]bool{}
	for name := range parseChecksums(bufio.NewScanner(f)) {
		if m := distVersionRE.FindStringSubmatch(name); m != nil {
			seen[m[1]] = true
		}
	}
	versions := make([]string, 0, len(seen))
	for v := range seen {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return versions, nil
}

// distCache keeps the parsed checksums.txt until its mtime or size changes.
type distCache struct {
	mu    sync.Mutex
	mtime time.Time
	size  int64
	names map[string]bool
}

// distListed returns the file names named in checksums.txt, plus itself.
func (a *app) distListed() (map[string]bool, error) {
	f, fi, err := openDistFile(a.distDir, "checksums.txt")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &a.distSums
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names != nil && c.mtime.Equal(fi.ModTime()) && c.size == fi.Size() {
		return c.names, nil
	}
	sc := bufio.NewScanner(f)
	names := parseChecksums(sc)
	if err := sc.Err(); err != nil {
		return nil, err
	}
	c.names, c.mtime, c.size = names, fi.ModTime(), fi.Size()
	return names, nil
}

func distContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".txt"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	}
	return "application/octet-stream"
}

func (a *app) distFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if a.distDir == "" || !distNameRE.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	listed, err := a.distListed()
	if err != nil || !listed[name] {
		http.NotFound(w, r)
		return
	}
	f, fi, err := openDistFile(a.distDir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", distContentType(name))
	h.Set("X-Content-Type-Options", "nosniff")
	if name == "checksums.txt" {
		h.Set("Cache-Control", "no-cache")
	} else {
		h.Set("Cache-Control", "public, max-age=300")
	}
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// requestOrigin is scheme://host as the client reached the server. The public
// URL wins; otherwise the Host header counts, and forwarded headers only from
// a trusted proxy.
func (a *app) requestOrigin(r *http.Request) (string, error) {
	if a.publicOrigin != "" {
		if !distHostRE.MatchString(strings.SplitN(a.publicOrigin, "://", 2)[1]) {
			return "", errors.New("bad public url")
		}
		if !strings.HasPrefix(a.publicOrigin, "https://") && !loopbackHost(strings.TrimPrefix(a.publicOrigin, "http://")) {
			return "", errPlainHTTP
		}
		return a.publicOrigin, nil
	}
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	fp, pset := singleForwardedValue(r.Header, "X-Forwarded-Proto")
	fh, hset := singleForwardedValue(r.Header, "X-Forwarded-Host")
	if pset || hset {
		if !a.proxies.Trusts(r.RemoteAddr) || !pset || !hset || (fp != "http" && fp != "https") || !validForwardedHost(fh) {
			return "", errors.New("bad forwarded headers")
		}
		scheme, host = fp, fh
	}
	if !validForwardedHost(host) || !distHostRE.MatchString(host) {
		return "", errors.New("bad host")
	}
	if scheme != "https" && !loopbackHost(host) {
		return "", errPlainHTTP
	}
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.Host != host {
		return "", errors.New("bad host")
	}
	return scheme + "://" + host, nil
}

var errPlainHTTP = errors.New("plain http")

// loopbackHost reports whether host (with optional port) is this machine.
func loopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	return strings.EqualFold(h, "localhost") || h == "127.0.0.1" || h == "::1"
}

// distHostRE keeps the host to characters that are inert inside a
// single-quoted shell string.
var distHostRE = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)

func (a *app) installSh(w http.ResponseWriter, r *http.Request) {
	if a.distDir == "" {
		http.NotFound(w, r)
		return
	}
	origin, err := a.requestOrigin(r)
	if errors.Is(err, errPlainHTTP) {
		http.Error(w, "install.sh is only served over https (set GHOSTTREE_PUBLIC_URL to the https address)", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The origin passed distHostRE or came from a validated public URL; the
	// escape changes nothing for such values and keeps the data flow explicit.
	origin = html.EscapeString(origin)
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(strings.ReplaceAll(installScript, installServerPlaceholder, origin)))
}

// ValidPublicHost reports whether host (with optional port) is made only of
// characters that are inert in a shell string.
func ValidPublicHost(host string) bool { return distHostRE.MatchString(host) }
