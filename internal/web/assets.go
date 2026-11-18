package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The static files of the interface (static/) are read, hashed and compressed
// once per process. A page names a file with assetURL, which appends the
// content hash (?v=…); such an address never changes its content, so the
// browser may keep it for a year. Every other request for a file is answered
// with an ETag and no-cache, so a stale address costs a 304 and never old code.

// assetBundles are the files every page loads, joined in the order of the
// slices: one request instead of many, cached across all pages. The order is
// the order the layout used to load them in; each script is a closed function,
// so joining them keeps what they share (window.GT*) and what they do not.
// shell.js (in the head) and localtime.js (run before the deferred scripts)
// stay single files because they are not deferred.
var assetBundles = map[string][]string{
	"bundle-base.css": {"ui-tokens.css", "ui-explain.css", "ui-base.css", "ui-shell.css", "ui-parts.css", "ui-pages.css", "ui-refs.css", "ui-rows.css"},
	"bundle-base.js":  {"live.js", "ui-tabs.js", "ui-pill.js", "ui-md.js", "ui-parts.js", "ui-select.js", "ui-refs.js", "ui-search.js", "ui-suggest.js", "ui-flash.js", "ui-toast.js", "ui-mentions.js"},
}

// vendorDir holds the third-party files (static/vendor/, see
// THIRD_PARTY_NOTICES.md). They are named "vendor/<file>" in the set and in the
// address, and they are the only static files that are not read at start in full:
// a file that big (Mermaid is 5.5 MB) is compressed when it is first asked for
// with gzip, not by every process that only starts.
const vendorDir = "vendor"

// asset is one file as it is sent: raw, and gzip when that is smaller.
type asset struct {
	body    []byte
	gz      []byte
	ctype   string
	hash    string // the full content hash, hex
	version string // the short form that goes into the address
	// lazyGz: gz is made by gzipped() on first use instead of by newAsset.
	lazyGz bool
	gzOnce sync.Once
}

// gzipped is the compressed body, or nil when compression does not make the file smaller.
func (a *asset) gzipped() []byte {
	if a.lazyGz {
		a.gzOnce.Do(func() { a.gz = gzipSmaller(a.body, a.ctype) })
	}
	return a.gz
}

type assetSet map[string]*asset

var loadAssets = sync.OnceValue(func() assetSet {
	set := assetSet{}
	entries, err := fs.ReadDir(files, "static")
	if err != nil {
		panic("web: static files: " + err.Error())
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(files, path.Join("static", e.Name()))
		if err != nil {
			panic("web: static file " + e.Name() + ": " + err.Error())
		}
		set[e.Name()] = newAsset(e.Name(), raw)
	}
	vendor, err := fs.ReadDir(files, "static/"+vendorDir)
	if err != nil {
		panic("web: static/" + vendorDir + ": " + err.Error())
	}
	for _, e := range vendor {
		if e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(files, path.Join("static", vendorDir, e.Name()))
		if err != nil {
			panic("web: static file " + vendorDir + "/" + e.Name() + ": " + err.Error())
		}
		a := newLazyAsset(vendorDir+"/"+e.Name(), raw)
		set[vendorDir+"/"+e.Name()] = a
	}
	for name, parts := range assetBundles {
		var joined bytes.Buffer
		for _, part := range parts {
			a, ok := set[part]
			if !ok {
				panic("web: bundle " + name + " names " + part + ", which is not in static/")
			}
			joined.Write(a.body)
			// A file that does not end in a newline must not run into the next.
			if !bytes.HasSuffix(a.body, []byte("\n")) {
				joined.WriteByte('\n')
			}
			// A script part that ends without a semicolon would otherwise be called
			// by the next one when that starts with "(" (automatic semicolon insertion).
			if path.Ext(name) == ".js" {
				joined.WriteString(";\n")
			}
		}
		set[name] = newAsset(name, joined.Bytes())
	}
	return set
})

func newAsset(name string, raw []byte) *asset {
	a := newLazyAsset(name, raw)
	a.lazyGz = false
	a.gz = gzipSmaller(raw, a.ctype)
	return a
}

// newLazyAsset hashes the file but leaves its compression to gzipped.
func newLazyAsset(name string, raw []byte) *asset {
	sum := sha256.Sum256(raw)
	a := &asset{body: raw, ctype: assetType(name), hash: hex.EncodeToString(sum[:]), lazyGz: compressible(assetType(name))}
	a.version = a.hash[:12]
	return a
}

// gzipSmaller is the gzip of a text file, or nil when the type is not text or the result is not smaller.
func gzipSmaller(raw []byte, ctype string) []byte {
	if !compressible(ctype) {
		return nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(raw)
	zw.Close()
	if buf.Len() < len(raw) {
		return buf.Bytes()
	}
	return nil
}

func assetType(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func compressible(ctype string) bool {
	return strings.HasPrefix(ctype, "text/") || strings.HasPrefix(ctype, "image/svg")
}

// assetURL is the address of a static file with its content hash; a name that
// is not there stays a plain address (and answers 404 when it is requested).
func assetURL(name string) string {
	if a, ok := loadAssets()[name]; ok {
		return "/static/" + name + "?v=" + a.version
	}
	return "/static/" + name
}

// acceptsGzip reports whether the request allows a gzip body (RFC 9110,
// Accept-Encoding): gzip named with a quality above 0, or, when gzip is not
// named, a * with a quality above 0.
func acceptsGzip(h http.Header) bool {
	gzipQ, starQ := -1.0, -1.0
	for _, field := range h.Values("Accept-Encoding") {
		for _, item := range strings.Split(field, ",") {
			coding, params, _ := strings.Cut(strings.TrimSpace(item), ";")
			q := 1.0
			if k, v, ok := strings.Cut(strings.ReplaceAll(strings.ToLower(params), " ", ""), "="); ok && k == "q" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					q = f
				}
			}
			switch strings.ToLower(strings.TrimSpace(coding)) {
			case "gzip", "x-gzip":
				gzipQ = q
			case "*":
				starQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

// staticFiles liefert die eingebetteten Dateien aus dem einmal berechneten
// Bestand (assets.go). Alles, was nicht darin steht, geht an den Dateiserver
// (Verzeichnisse, fehlende Dateien).
func (a *app) staticFiles() http.Handler {
	fallback := http.FileServerFS(files)
	set := loadAssets()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, "/static/")
		f := set[name]
		if !ok || f == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			fallback.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", f.ctype)
		h.Set("X-Content-Type-Options", "nosniff")
		if v := r.URL.Query().Get("v"); v != "" && v == f.version {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		body, etag := f.body, `"`+f.hash+`"`
		// Vary is set before the compression is made: a client that does not ask for gzip costs none.
		if f.lazyGz || f.gz != nil {
			h.Set("Vary", "Accept-Encoding")
			if acceptsGzip(r.Header) {
				if gz := f.gzipped(); gz != nil {
					body, etag = gz, `"`+f.hash+`-gzip"`
					h.Set("Content-Encoding", "gzip")
				}
			}
		}
		h.Set("ETag", etag)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}
