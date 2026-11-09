// Package selfupdate fetches a new ctx binary, checks it and swaps it in.
// Nothing is installed or run before every check has passed: a failed check
// leaves the installed binary and its directory untouched.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Deadweight-Labs/ghosttree/internal/version"
)

const (
	maxTextBytes    = 1 << 20
	maxArchiveBytes = 300 << 20
	maxBinaryBytes  = 600 << 20
	keepBackups     = 3
)

// Verifier checks that checksumsFile was signed by the release workflow of tag.
// It returns nil only when the signature is valid; "cannot check" is an error.
type Verifier func(ctx context.Context, checksumsFile, bundleFile, tag string) error

// Source is one place to fetch from. HTTP is the client to use (tests pass
// httptest's).
type Source struct {
	HTTP         *http.Client
	GOOS, GOARCH string
}

// Candidate is a verified-to-exist release or build that could be installed.
type Candidate struct {
	Version  string // without leading v
	Tag      string // stable only
	Archive  string // file name, ctx_<version>_<os>_<arch>.tar.gz
	archURL  string
	sumsURL  string
	bundle   string // stable only: URL of checksums.txt.sigstore.json
	Channel  string
	Where    string // human description of the origin
	signed   bool
	repoSlug string
}

func (s Source) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return http.DefaultClient
}

// archiveName ist der Name der Archive, die scripts/build-dist.sh baut und der
// Server unter /dist/ anbietet.
func (s Source) archiveName(v string) string {
	return fmt.Sprintf("ctx_%s_%s_%s.tar.gz", v, s.GOOS, s.GOARCH)
}

// releaseArchiveName ist der Name der Archive an einem GitHub-Release: goreleaser
// benennt sie nach dem Projekt (.goreleaser.yaml, name_template).
func (s Source) releaseArchiveName(v string) string {
	return fmt.Sprintf("ghosttree_%s_%s_%s.tar.gz", v, s.GOOS, s.GOARCH)
}

func (s Source) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ctx-update")
	resp, err := s.client().Do(req)
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

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// LatestStable finds the newest published, non-prerelease release of repo
// ("owner/name") through the GitHub release API at apiBase.
func (s Source) LatestStable(ctx context.Context, apiBase, repo string) (Candidate, error) {
	return s.LatestRelease(ctx, apiBase, repo, false)
}

// LatestRelease is LatestStable with the choice of ctx update --pre: with pre
// the release candidates (tags like v0.1.0-rc.3, published as prereleases) are
// candidates as well, and the newest version wins, so v0.1.0 beats
// v0.1.0-rc.3. Other pre-release tags (-dev, -beta) stay out either way. The
// checks in Fetch do not depend on the choice: a candidate is signed and its
// checksum is compared exactly as for a final release.
func (s Source) LatestRelease(ctx context.Context, apiBase, repo string, pre bool) (Candidate, error) {
	b, err := s.get(ctx, strings.TrimSuffix(apiBase, "/")+"/repos/"+repo+"/releases?per_page=50", maxTextBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("release list: %w", err)
	}
	var rels []ghRelease
	if err := json.Unmarshal(b, &rels); err != nil {
		return Candidate{}, fmt.Errorf("release list: %w", err)
	}
	var best *ghRelease
	for i := range rels {
		r := &rels[i]
		v := strings.TrimPrefix(r.TagName, "v")
		if r.Draft || version.Channel(v) != version.ChannelStable {
			continue
		}
		if !pre && (r.Prerelease || strings.Contains(v, "-")) {
			continue
		}
		if best == nil || version.Compare(strings.TrimPrefix(best.TagName, "v"), v) == version.Newer {
			best = r
		}
	}
	if best == nil {
		if pre {
			return Candidate{}, errors.New("no release or release candidate published yet")
		}
		return Candidate{}, errors.New("no stable release published yet (release candidates are not installed by the stable channel; --pre includes them)")
	}
	v := strings.TrimPrefix(best.TagName, "v")
	c := Candidate{Version: v, Tag: best.TagName, Archive: s.archiveName(v), Channel: version.ChannelStable,
		Where: "github.com/" + repo + " release " + best.TagName, signed: true, repoSlug: repo}
	for _, a := range best.Assets {
		switch a.Name {
		case s.releaseArchiveName(v):
			// GitHub-Releases (goreleaser, Projektname) heißen ghosttree_…; der
			// Name geht vor, ein ctx_-Archiv gilt nur, wenn es keins gibt.
			c.Archive, c.archURL = a.Name, a.URL
		case c.Archive:
			if c.archURL == "" {
				c.archURL = a.URL
			}
		case "checksums.txt":
			c.sumsURL = a.URL
		case "checksums.txt.sigstore.json":
			c.bundle = a.URL
		}
	}
	switch {
	case c.archURL == "":
		return c, fmt.Errorf("release %s has no asset %s", best.TagName, c.Archive)
	case c.sumsURL == "":
		return c, fmt.Errorf("release %s has no checksums.txt", best.TagName)
	case c.bundle == "":
		return c, fmt.Errorf("release %s has no checksums.txt.sigstore.json: unsigned, refusing", best.TagName)
	}
	return c, nil
}

var distVersionRE = regexp.MustCompile(`^ctx_(.+)_(linux|darwin)_(amd64|arm64)\.tar\.gz$`)

// DevFromServer reads the dev build the server offers under /dist/. The trust
// here is the connection (https, or loopback) plus the checksum from the same
// server; there is no signature for it.
func (s Source) DevFromServer(ctx context.Context, serverURL string) (Candidate, error) {
	u, err := url.Parse(strings.TrimSuffix(serverURL, "/"))
	if err != nil || u.Host == "" {
		return Candidate{}, fmt.Errorf("server url %q is not usable", serverURL)
	}
	if u.Scheme != "https" && !loopback(u.Host) {
		return Candidate{}, errors.New("refusing to fetch a binary over plain http from a non-local server")
	}
	base := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/") + "/dist/"
	sums, err := s.get(ctx, base+"checksums.txt", maxTextBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("server offers no dev build: %w", err)
	}
	versions := map[string]bool{}
	for name := range ParseChecksums(sums) {
		if m := distVersionRE.FindStringSubmatch(name); m != nil {
			versions[m[1]] = true
		}
	}
	if len(versions) == 0 {
		return Candidate{}, errors.New("the server's checksums.txt lists no ctx archive")
	}
	if len(versions) > 1 {
		return Candidate{}, errors.New("the server's checksums.txt lists more than one ctx version; ambiguous, refusing")
	}
	var v string
	for k := range versions {
		v = k
	}
	arch := s.archiveName(v)
	if _, ok := ParseChecksums(sums)[arch]; !ok {
		return Candidate{}, fmt.Errorf("the server offers no build for %s/%s", s.GOOS, s.GOARCH)
	}
	return Candidate{Version: v, Archive: arch, Channel: version.ChannelDev, Where: u.Host + "/dist/",
		archURL: base + arch, sumsURL: base + "checksums.txt"}, nil
}

func loopback(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	return strings.EqualFold(h, "localhost") || h == "127.0.0.1" || h == "::1"
}

// ParseChecksums reads sha256sum lines into name -> hash. Malformed lines are
// dropped, names with a path are dropped.
func ParseChecksums(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	hashRE := regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	for sc.Scan() {
		f := strings.Fields(strings.TrimRight(sc.Text(), "\r"))
		if len(f) != 2 || !hashRE.MatchString(f[0]) {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			continue
		}
		out[name] = strings.ToLower(f[0])
	}
	return out
}

// Fetch downloads, verifies and extracts the candidate's binary into a new
// temporary file inside dir (mode 0755) and returns its path. Verification
// order: signature over checksums.txt (stable only), then the archive's
// sha256 against that checksums.txt. On any failure the temporary file is
// removed and nothing else was touched.
func (s Source) Fetch(ctx context.Context, c Candidate, dir string, verify Verifier) (string, error) {
	sums, err := s.get(ctx, c.sumsURL, maxTextBytes)
	if err != nil {
		return "", fmt.Errorf("checksums: %w", err)
	}
	if c.signed {
		if verify == nil {
			return "", errors.New("no signature verifier available; refusing to install an unchecked release")
		}
		bundle, err := s.get(ctx, c.bundle, maxTextBytes)
		if err != nil {
			return "", fmt.Errorf("signature bundle: %w", err)
		}
		scratch, err := os.MkdirTemp("", "ctx-update-verify-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(scratch)
		sf, bf := filepath.Join(scratch, "checksums.txt"), filepath.Join(scratch, "checksums.txt.sigstore.json")
		if err := os.WriteFile(sf, sums, 0o600); err != nil {
			return "", err
		}
		if err := os.WriteFile(bf, bundle, 0o600); err != nil {
			return "", err
		}
		if err := verify(ctx, sf, bf, c.Tag); err != nil {
			return "", fmt.Errorf("signature check failed: %w", err)
		}
	}
	want, ok := ParseChecksums(sums)[c.Archive]
	if !ok {
		return "", fmt.Errorf("checksums.txt does not list %s", c.Archive)
	}
	archive, err := s.get(ctx, c.archURL, maxArchiveBytes)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("checksum mismatch for %s: checksums.txt says %s, download is %s", c.Archive, want, hex.EncodeToString(got[:]))
	}
	return extractBinary(archive, dir)
}

// extractBinary writes the "ctx" entry of a tar.gz into a temp file in dir.
func extractBinary(archive []byte, dir string) (string, error) {
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
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "ctx" || h.Size <= 0 || h.Size > maxBinaryBytes {
			continue
		}
		tmp, err := os.CreateTemp(dir, ".ctx-update-*")
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

// Install swaps newPath (a file in the same directory as target) in for
// target. The old binary stays as <target>.prev-<oldVersion> (hard link, so it
// is also what running processes still execute); only the newest few backups
// are kept. The rename is atomic: target is always either old or new.
func Install(target, newPath, oldVersion string) (backup string, err error) {
	if filepath.Dir(newPath) != filepath.Dir(target) {
		return "", errors.New("new binary must sit in the target directory for an atomic rename")
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		return "", err
	}
	safe := regexp.MustCompile(`[^A-Za-z0-9._+-]`).ReplaceAllString(oldVersion, "-")
	backup = target + ".prev-" + safe
	_ = os.Remove(backup)
	if err := os.Link(target, backup); err != nil {
		if err := copyFile(target, backup); err != nil {
			return "", fmt.Errorf("backup: %w", err)
		}
	}
	if err := os.Rename(newPath, target); err != nil {
		os.Remove(backup)
		return "", err
	}
	pruneBackups(target, backup)
	return backup, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if e := out.Close(); cerr == nil {
		cerr = e
	}
	return cerr
}

func pruneBackups(target, keep string) {
	old, _ := filepath.Glob(target + ".prev-*")
	type bk struct {
		path string
		mod  int64
	}
	var all []bk
	for _, p := range old {
		if fi, err := os.Lstat(p); err == nil {
			all = append(all, bk{p, fi.ModTime().UnixNano()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod > all[j].mod })
	for i, b := range all {
		if i >= keepBackups && b.path != keep {
			os.Remove(b.path)
		}
	}
}
