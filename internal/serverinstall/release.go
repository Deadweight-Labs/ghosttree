// Package serverinstall sets up and updates a Ghosttree server on a machine:
// the files, the systemd units, the first account, and the release that runs.
// ctx server install and ctx server update are thin commands around it.
package serverinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/minisign"
)

// Release says where releases come from. The defaults are GitHub's; the fields
// exist so tests (and a mirror) can point elsewhere. The key is never
// configurable here: what is fetched is trusted only if the release key signed it.
type Release struct {
	Repo    string // owner/name, default Deadweight-Labs/ghosttree
	APIBase string // default https://api.github.com
	DLBase  string // <DLBase>/<tag>/<file>, default https://github.com/<repo>/releases/download
	Key     minisign.PublicKey
	HTTP    *http.Client // default http.DefaultClient
	GOOS    string       // default runtime.GOOS
	GOARCH  string       // default runtime.GOARCH
}

const (
	defaultRepo = "Deadweight-Labs/ghosttree"
	maxText     = 1 << 20
	maxArchive  = 300 << 20
	maxBinary   = 600 << 20
)

var tagRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)

func (r Release) repo() string   { return orDefault(r.Repo, defaultRepo) }
func (r Release) goos() string   { return orDefault(r.GOOS, runtime.GOOS) }
func (r Release) goarch() string { return orDefault(r.GOARCH, runtime.GOARCH) }
func (r Release) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return http.DefaultClient
}
func (r Release) api() string {
	return strings.TrimRight(orDefault(r.APIBase, "https://api.github.com"), "/")
}
func (r Release) dl() string {
	return strings.TrimRight(orDefault(r.DLBase, "https://github.com/"+r.repo()+"/releases/download"), "/")
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// ValidTag reports whether tag is a release tag this code installs.
func ValidTag(tag string) bool { return tagRE.MatchString(tag) }

func (r Release) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	if err := httpsOrLoopback(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ctx-server")
	// A redirect is checked like the first address: never from https to plain
	// http, and plain http only to this machine.
	client := *r.client()
	client.CheckRedirect = checkRedirect
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: answer larger than %d bytes", u, limit)
	}
	return b, nil
}

// checkRedirect is the redirect policy of release downloads.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if err := httpsOrLoopback(req.URL.String()); err != nil {
		return fmt.Errorf("refusing a redirect: %w", err)
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect from https to plain http (%s)", req.URL.Host)
	}
	return nil
}

// httpsOrLoopback refuses plain http except to this machine, as install.sh does.
// url.URL.Hostname leaves out the user information, so "http://localhost:1@example.org"
// is the host example.org here, as it is for the client that sends the request.
func httpsOrLoopback(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
	}
	return fmt.Errorf("refusing to download over plain http from %s", u.Host)
}

// Latest asks for the newest stable release (a release candidate is only
// installed when its tag is named).
func (r Release) Latest(ctx context.Context) (string, error) {
	b, err := r.get(ctx, r.api()+"/repos/"+r.repo()+"/releases/latest", maxText)
	if err != nil {
		return "", fmt.Errorf("there is no stable ghosttree release yet, or it cannot be reached (%w); name a release candidate with --version, for example v0.1.0-rc.3", err)
	}
	var v struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(b, &v); err != nil || !ValidTag(v.Tag) {
		return "", fmt.Errorf("the release list names no usable tag (%q)", v.Tag)
	}
	return v.Tag, nil
}

// Fetched is a release whose files passed every check.
type Fetched struct {
	Tag, Version string
	Binary       string // path of the extracted ctx, in the directory given to Fetch
}

// Fetch downloads tag, checks the minisign signature of checksums.txt against
// r.Key and the trusted comment against the tag, compares the archive's SHA-256
// and extracts ctx into dir (a file next to where it will be installed, so a
// rename is atomic). On any failure nothing is left behind.
func (r Release) Fetch(ctx context.Context, tag, dir string) (*Fetched, error) {
	if !ValidTag(tag) {
		return nil, fmt.Errorf("unexpected release tag %q (expected v1.2.3 or v1.2.3-rc.1)", tag)
	}
	base := r.dl() + "/" + tag
	sums, err := r.verifiedChecksums(ctx, tag)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(tag, "v")
	suffix := "_" + r.goos() + "_" + r.goarch() + ".tar.gz"
	archive, want, err := archiveEntry(sums, suffix)
	if err != nil {
		return nil, fmt.Errorf("checksums of %s: %w", tag, err)
	}
	data, err := r.get(ctx, base+"/"+archive, maxArchive)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s (expected %s, got %s); nothing was installed", archive, want, hex.EncodeToString(got[:]))
	}
	bin, err := extractCtx(data, dir)
	if err != nil {
		return nil, err
	}
	return &Fetched{Tag: tag, Version: version, Binary: bin}, nil
}

// verifiedChecksums downloads checksums.txt of tag and returns it only if its
// minisign signature is made by r.Key and its trusted comment names this tag.
func (r Release) verifiedChecksums(ctx context.Context, tag string) ([]byte, error) {
	if !ValidTag(tag) {
		return nil, fmt.Errorf("unexpected release tag %q (expected v1.2.3 or v1.2.3-rc.1)", tag)
	}
	base := r.dl() + "/" + tag
	sums, err := r.get(ctx, base+"/checksums.txt", maxText)
	if err != nil {
		return nil, fmt.Errorf("checksums of %s: %w", tag, err)
	}
	sig, err := r.get(ctx, base+"/checksums.txt.minisig", maxText)
	if err != nil {
		return nil, fmt.Errorf("a release without a signature is not installed: %w", err)
	}
	comment, err := minisign.Verify(r.Key, sums, sig)
	if err != nil {
		return nil, fmt.Errorf("the signature of checksums.txt does not match the ghosttree release key; nothing was installed (%w)", err)
	}
	if comment != "ghosttree "+tag {
		return nil, fmt.Errorf("the signed checksums.txt belongs to %q, not to ghosttree %s; nothing was installed", comment, tag)
	}
	return sums, nil
}

// VerifyTag checks that tag is a release the release key signed, without
// downloading anything else: what a Docker update needs, since the image comes
// from the registry and only the tag is vouched for here.
func (r Release) VerifyTag(ctx context.Context, tag string) error {
	_, err := r.verifiedChecksums(ctx, tag)
	return err
}

// archiveEntry finds the single ghosttree_… or ctx_… archive for this platform
// in a checksums.txt and its hash.
func archiveEntry(sums []byte, suffix string) (name, hash string, err error) {
	hashRE := regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	var hits [][2]string
	for _, line := range strings.Split(strings.ReplaceAll(string(sums), "\r", ""), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !hashRE.MatchString(f[0]) {
			continue
		}
		n := strings.TrimPrefix(f[1], "*")
		if (strings.HasPrefix(n, "ghosttree_") || strings.HasPrefix(n, "ctx_")) && strings.HasSuffix(n, suffix) && len(n) > len(suffix) {
			hits = append(hits, [2]string{n, strings.ToLower(f[0])})
		}
	}
	switch len(hits) {
	case 0:
		return "", "", fmt.Errorf("no ctx build for %s listed", strings.TrimSuffix(strings.TrimPrefix(suffix, "_"), ".tar.gz"))
	case 1:
		n := hits[0][0]
		if strings.ContainsAny(n, "/\\") || strings.Contains(n, "..") || strings.HasPrefix(n, "-") {
			return "", "", errors.New("unexpected archive name")
		}
		return n, hits[0][1], nil
	}
	return "", "", fmt.Errorf("%d builds listed for one platform; refusing an ambiguous list", len(hits))
}

func extractCtx(archive []byte, dir string) (string, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", errors.New("archive holds no file named ctx")
		}
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "ctx" || h.Size <= 0 || h.Size > maxBinary {
			continue
		}
		tmp, err := os.CreateTemp(dir, ".ctx-new-*")
		if err != nil {
			return "", err
		}
		_, cerr := io.Copy(tmp, io.LimitReader(tr, h.Size))
		if e := tmp.Close(); cerr == nil {
			cerr = e
		}
		if cerr == nil {
			cerr = os.Chmod(tmp.Name(), 0o755)
		}
		if cerr != nil {
			os.Remove(tmp.Name())
			return "", cerr
		}
		return tmp.Name(), nil
	}
}
