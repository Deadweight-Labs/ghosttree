package web

import (
	"bufio"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed install.sh
var installScript string

const installServerPlaceholder = "__GHOSTTREE_SERVER__"

// WithDistDir points the server at a directory holding the ctx release
// archives (ctx_<version>_<os>_<arch>.tar.gz) and their checksums.txt, in the
// layout goreleaser writes. Without it /install.sh and /dist/ answer 404.
func WithDistDir(dir string) Option { return func(a *app) { a.distDir = dir } }

var distNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// distListed returns the file names named in checksums.txt, plus checksums.txt.
// A name with a path separator or anything outside a plain file name is
// dropped, so the list can never point out of the directory.
func (a *app) distListed() (map[string]bool, error) {
	f, err := os.Open(filepath.Join(a.distDir, "checksums.txt"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names := map[string]bool{"checksums.txt": true}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if distNameRE.MatchString(name) && !strings.Contains(name, "..") {
			names[name] = true
		}
	}
	return names, sc.Err()
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
	path := filepath.Join(a.distDir, name)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
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
	u, err := url.Parse(scheme + "://" + host)
	if err != nil || u.Host != host {
		return "", errors.New("bad host")
	}
	return scheme + "://" + host, nil
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
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/x-shellscript; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(strings.ReplaceAll(installScript, installServerPlaceholder, origin)))
}
