package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// Profile pictures (REQ-599 criterion 14). A person with a linked GitHub
// account (store.AccountGitHub) gets their GitHub picture everywhere an avatar
// stands. The browser never loads it from GitHub: the server fetches it, keeps
// it for a day (on disk when a directory is set, else in memory) and serves it
// itself at /ui/avatar/{account}, so the CSP stays 'self'. Without a link, or
// when the fetch fails, the avatar stays the initials.
//
// What the server fetches is fixed in the code, never taken from data: the
// picture by number from avatars.githubusercontent.com, or by login from
// github.com/<login>.png whose redirect may lead to that one host and nowhere
// else. The login has GitHub's syntax before it goes into an address. The fetch
// has a timeout and a size limit and must come back as a raster image.

const (
	avatarHost      = "avatars.githubusercontent.com"
	avatarTTL       = 24 * time.Hour
	avatarRetryWait = 15 * time.Minute
	avatarMaxBytes  = 1 << 20
	// avatarPxText is the edge the picture is fetched at: the largest avatar (160 px
	// on the profile) at twice the pixel density. It is part of the file name on
	// disk, so a change of size does not serve the old, smaller pictures.
	avatarPxText  = "320"
	avatarTimeout = 5 * time.Second
)

// avatarTypes are the image types that are served. SVG is not one of them: it
// can carry script.
var avatarTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// avatarCache fetches and keeps the pictures.
type avatarCache struct {
	store  *store.Store
	client *http.Client
	dir    string

	mu     sync.Mutex
	mem    map[int64]avatarEntry
	failed map[int64]time.Time
	busy   map[int64]bool
	names  map[string]avatarName
	// syncFirst makes the first look at a picture wait for the fetch instead of
	// starting it in the background (tests).
	syncFirst bool
	now       func() time.Time
}

type avatarEntry struct {
	Data    []byte
	Type    string
	Fetched time.Time
}

type avatarName struct {
	ID      int64
	Checked time.Time
}

func newAvatarCache(st *store.Store, client *http.Client, dir string) *avatarCache {
	if client == nil {
		client = avatarClient(nil)
	}
	return &avatarCache{store: st, client: client, dir: dir, mem: map[int64]avatarEntry{}, failed: map[int64]time.Time{},
		busy: map[int64]bool{}, names: map[string]avatarName{}, now: time.Now}
}

// avatarClient is the HTTP client of the fetch: a timeout, and redirects only to
// the picture host. rt replaces the transport (tests).
func avatarClient(rt http.RoundTripper) *http.Client {
	c := &http.Client{Timeout: avatarTimeout, Transport: rt}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 2 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" || req.URL.Hostname() != avatarHost || req.URL.Port() != "" {
			return fmt.Errorf("redirect to %s refused", req.URL.Host)
		}
		return nil
	}
	return c
}

// avatarURL is the address the picture of a linked account comes from.
func avatarURL(l store.GitHubLink) (string, bool) {
	if l.ID > 0 {
		return "https://" + avatarHost + "/u/" + strconv.FormatInt(l.ID, 10) + "?s=" + avatarPxText + "&v=4", true
	}
	if store.ValidGitHubLogin(l.Login) {
		return "https://github.com/" + url.PathEscape(l.Login) + ".png?size=" + avatarPxText, true
	}
	return "", false
}

// fetch loads the picture from GitHub: status 200, a raster image, at most
// avatarMaxBytes, the type taken from the bytes and not from the header alone.
func (c *avatarCache) fetch(link store.GitHubLink) (avatarEntry, error) {
	target, ok := avatarURL(link)
	if !ok {
		return avatarEntry{}, errors.New("no address")
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return avatarEntry{}, err
	}
	req.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp")
	req.Header.Set("User-Agent", "ghosttree")
	resp, err := c.client.Do(req)
	if err != nil {
		return avatarEntry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return avatarEntry{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	if final := resp.Request.URL; final.Scheme != "https" || (final.Hostname() != avatarHost && final.Hostname() != "github.com") {
		return avatarEntry{}, fmt.Errorf("answered from %s", final.Host)
	}
	declared, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	if !avatarTypes[strings.ToLower(strings.TrimSpace(declared))] {
		return avatarEntry{}, fmt.Errorf("type %q is not served", declared)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, avatarMaxBytes+1))
	if err != nil {
		return avatarEntry{}, err
	}
	if len(data) > avatarMaxBytes || len(data) == 0 {
		return avatarEntry{}, errors.New("empty or too large")
	}
	sniffed, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if !avatarTypes[sniffed] {
		return avatarEntry{}, fmt.Errorf("content is %q", sniffed)
	}
	return avatarEntry{Data: data, Type: sniffed, Fetched: c.now()}, nil
}

func (c *avatarCache) path(id int64, ext string) string {
	return filepath.Join(c.dir, strconv.FormatInt(id, 10)+"-"+avatarPxText+ext)
}

type avatarMeta struct {
	Type    string `json:"type"`
	Fetched int64  `json:"fetched"`
}

// load reads an entry from memory or from disk.
func (c *avatarCache) load(id int64) (avatarEntry, bool) {
	c.mu.Lock()
	e, ok := c.mem[id]
	c.mu.Unlock()
	if ok {
		return e, true
	}
	if c.dir == "" {
		return avatarEntry{}, false
	}
	rawMeta, err := os.ReadFile(c.path(id, ".json"))
	if err != nil {
		return avatarEntry{}, false
	}
	var m avatarMeta
	if json.Unmarshal(rawMeta, &m) != nil || !avatarTypes[m.Type] {
		return avatarEntry{}, false
	}
	data, err := os.ReadFile(c.path(id, ".img"))
	if err != nil || len(data) == 0 || len(data) > avatarMaxBytes {
		return avatarEntry{}, false
	}
	e = avatarEntry{Data: data, Type: m.Type, Fetched: time.Unix(m.Fetched, 0)}
	c.mu.Lock()
	c.mem[id] = e
	c.mu.Unlock()
	return e, true
}

// save keeps an entry in memory and, with a directory, on disk (written to a
// temporary file first, so a reader never sees half a picture).
func (c *avatarCache) save(id int64, e avatarEntry) {
	c.mu.Lock()
	c.mem[id] = e
	c.mu.Unlock()
	if c.dir == "" || os.MkdirAll(c.dir, 0o700) != nil {
		return
	}
	meta, _ := json.Marshal(avatarMeta{Type: e.Type, Fetched: e.Fetched.Unix()})
	for _, f := range []struct {
		ext  string
		data []byte
	}{{".img", e.Data}, {".json", meta}} {
		tmp, err := os.CreateTemp(c.dir, "tmp-*")
		if err != nil {
			return
		}
		_, werr := tmp.Write(f.data)
		cerr := tmp.Close()
		if werr != nil || cerr != nil || os.Rename(tmp.Name(), c.path(id, f.ext)) != nil {
			os.Remove(tmp.Name())
			return
		}
	}
}

func (c *avatarCache) fresh(e avatarEntry) bool { return c.now().Sub(e.Fetched) < avatarTTL }

// refresh fetches the picture of an account now and keeps it. A failure is
// remembered for a while so a down GitHub is not asked again for every page.
func (c *avatarCache) refresh(id int64) (avatarEntry, bool) {
	link, ok := c.store.AccountGitHub("person:" + strconv.FormatInt(id, 10))
	if !ok {
		return avatarEntry{}, false
	}
	e, err := c.fetch(link)
	if err != nil {
		c.mu.Lock()
		c.failed[id] = c.now()
		c.mu.Unlock()
		return avatarEntry{}, false
	}
	// The link may have been removed or changed while the picture came in; then
	// the picture belongs to nobody any more and must not be kept.
	if now, still := c.store.AccountGitHub("person:" + strconv.FormatInt(id, 10)); !still || now != link {
		return avatarEntry{}, false
	}
	c.save(id, e)
	c.mu.Lock()
	delete(c.failed, id)
	c.mu.Unlock()
	return e, true
}

// forget drops everything kept for an account: the picture in memory and on
// disk, the pause after a failed fetch and the remembered name lookups (a name
// stays "linked" or "not linked" for a minute otherwise). Used when the link
// changes, so the next page shows what the link says now.
func (c *avatarCache) forget(id int64) {
	c.mu.Lock()
	delete(c.mem, id)
	delete(c.failed, id)
	c.names = map[string]avatarName{}
	c.mu.Unlock()
	if c.dir != "" {
		os.Remove(c.path(id, ".img"))
		os.Remove(c.path(id, ".json"))
	}
}

// backoff: the last fetch failed a short while ago.
func (c *avatarCache) backoff(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.failed[id]
	return ok && c.now().Sub(at) < avatarRetryWait
}

// claim takes the one fetch an account may have running; false means another
// is on its way.
func (c *avatarCache) claim(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy[id] {
		return false
	}
	c.busy[id] = true
	return true
}

func (c *avatarCache) release(id int64) {
	c.mu.Lock()
	delete(c.busy, id)
	c.mu.Unlock()
}

// refreshOnce fetches now, but only when no pause is running after a failure and
// no other fetch of this account is on its way: whoever asks while one runs gets
// what is cached, not a second request to GitHub.
func (c *avatarCache) refreshOnce(id int64) (avatarEntry, bool) {
	if c.backoff(id) || !c.claim(id) {
		return avatarEntry{}, false
	}
	defer c.release(id)
	return c.refresh(id)
}

// refreshLater starts a fetch in the background under the same two rules.
func (c *avatarCache) refreshLater(id int64) {
	if c.backoff(id) || !c.claim(id) {
		return
	}
	go func() {
		defer c.release(id)
		c.refresh(id)
	}()
}

// ready says whether a picture of the account can be shown now. A stale picture
// still shows while a new one is fetched; a missing one makes the page show the
// initials this time and be right the next.
func (c *avatarCache) ready(id int64) bool {
	e, ok := c.load(id)
	if ok && c.fresh(e) {
		return true
	}
	if c.syncFirst {
		if _, got := c.refreshOnce(id); got {
			return true
		}
		return ok
	}
	c.refreshLater(id)
	return ok
}

// accountOf is the account whose display name is name, 0 for none or for one
// without a linked GitHub account. The answer is kept for a minute.
func (c *avatarCache) accountOf(name string) int64 {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	c.mu.Lock()
	n, ok := c.names[name]
	c.mu.Unlock()
	if ok && c.now().Sub(n.Checked) < time.Minute {
		return n.ID
	}
	var id int64
	if acct, err := c.store.AccountByName(name); err == nil {
		if num, ok := store.AccountNumericID(acct.ID); ok {
			if _, linked := c.store.AccountGitHub(acct.ID); linked {
				id = num
			}
		}
	}
	c.mu.Lock()
	c.names[name] = avatarName{ID: id, Checked: c.now()}
	c.mu.Unlock()
	return id
}

// avatarSpan is the mark the avatar functions write for a person (identity.go).
var avatarSpan = regexp.MustCompile(`<span class="(gav [^"]*|own)" data-av="([^"]*)" title="([^"]*)">[^<]*</span>`)

// withPhotos replaces the initials of the people who have a picture by the
// picture, which comes from this server. Everything else stays as written.
func (c *avatarCache) withPhotos(page []byte) []byte {
	if !bytes.Contains(page, []byte(`data-av="`)) {
		return page
	}
	return avatarSpan.ReplaceAllFunc(page, func(m []byte) []byte {
		sub := avatarSpan.FindSubmatch(m)
		name := html.UnescapeString(string(sub[2]))
		id := c.accountOf(name)
		if id == 0 || !c.ready(id) {
			return m
		}
		class := string(sub[1])
		if class != "own" {
			var keep []string
			for _, f := range strings.Fields(class) {
				if !strings.HasPrefix(f, "hc-") {
					keep = append(keep, f)
				}
			}
			class = strings.Join(append(keep, "ph"), " ")
		}
		return []byte(`<img class="` + class + `" src="/ui/avatar/` + strconv.FormatInt(id, 10) + `" alt="" title="` + string(sub[3]) + `">`)
	})
}

// avatarImage is /ui/avatar/{id}: the picture of a linked account, from the
// cache, fetched when there is none. Unknown, unlinked, failed and an account the
// viewer shares no organisation or project with are all 404.
func (a *app) avatarImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || a.avatars == nil {
		http.NotFound(w, r)
		return
	}
	if !a.access(r).SharesContextWith(id) {
		// Serial ids: a picture for a stranger would tell that the account exists.
		http.NotFound(w, r)
		return
	}
	c := a.avatars
	if _, linked := a.store.AccountGitHub("person:" + strconv.FormatInt(id, 10)); !linked {
		// A picture saved while the link was being removed belongs to nobody.
		http.NotFound(w, r)
		return
	}
	e, ok := c.load(id)
	if !ok || !c.fresh(e) {
		// Not more often than a page would: the pause after a failure and the
		// one running fetch per account hold here too, so the route cannot be
		// used to make the server ask GitHub again and again.
		if fetched, got := c.refreshOnce(id); got {
			e, ok = fetched, true
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", e.Type)
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(e.Data)))
	_, _ = w.Write(e.Data)
}

// WithAvatarCache sets the directory in which the fetched profile pictures are
// kept. Without it they are kept in memory only.
func WithAvatarCache(dir string) Option {
	return func(a *app) { a.avatarDir = dir }
}

// withAvatarClient replaces the HTTP client of the picture fetch (tests).
func withAvatarClient(c *http.Client) Option {
	return func(a *app) { a.avatarClient = c }
}
