package embed_test

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Deadweight-Labs/ghosttree/internal/embed"
	"github.com/Deadweight-Labs/ghosttree/internal/embed/embedtest"
)

func TestTextsFollowTheModelCard(t *testing.T) {
	if got, want := embed.QueryText("  wie  funktioniert\nden Hook? "), "task: search result | query: wie funktioniert den Hook?"; got != want {
		t.Fatalf("query text = %q, want %q", got, want)
	}
	if got, want := embed.DocumentText("Der Titel", "Der Körper."), "title: Der Titel | text: Der Körper."; got != want {
		t.Fatalf("document text = %q, want %q", got, want)
	}
	if got, want := embed.DocumentText("  ", "nur Text"), "title: none | text: nur Text"; got != want {
		t.Fatalf("document without title = %q, want %q", got, want)
	}
	if embed.PrefixVersion == "" || embed.DefaultModel == "" {
		t.Fatal("model and prefix version identify every stored vector")
	}
}

func TestDocumentTextIsCutOnACharacterBoundary(t *testing.T) {
	body := strings.Repeat("äöü€😀", 1000)
	got := embed.DocumentText("t", body)
	if n := utf8.RuneCountInString(got); n > embed.MaxDocumentRunes {
		t.Fatalf("document has %d characters, the cap is %d", n, embed.MaxDocumentRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("the cut split a character")
	}
	long := embed.DocumentText(strings.Repeat("T", 5000), "body")
	if n := utf8.RuneCountInString(long); n > embed.MaxDocumentRunes {
		t.Fatalf("a long title lets the document grow to %d characters", n)
	}
	if !strings.HasSuffix(long, "text: body") {
		t.Fatalf("a long title must not eat the body: %q", long[len(long)-30:])
	}
	if a, b := embed.TextHash(embed.DocumentText("a", "b")), embed.TextHash(embed.DocumentText("a", "c")); a == b || len(a) != 64 {
		t.Fatalf("hashes %q %q", a, b)
	}
	if embed.DocumentText("t", "x\xff\xfey") == "" || !utf8.ValidString(embed.DocumentText("t\xff", "x\xff")) {
		t.Fatal("invalid UTF-8 must be replaced, not sent")
	}
}

func TestParseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8475":                "http://127.0.0.1:8475",
		"http://127.0.0.1:8475/":               "http://127.0.0.1:8475",
		" http://localhost:8475/v1/embeddings": "http://localhost:8475",
		"https://emb.example/api/":             "https://emb.example/api",
	} {
		got, err := embed.ParseURL(in)
		if err != nil || got != want {
			t.Errorf("ParseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "127.0.0.1:8475", "ftp://x", "http://", "http://user:pw@host", "http://h/?a=b", "http://h/#f"} {
		if _, err := embed.ParseURL(in); err == nil {
			t.Errorf("ParseURL(%q) accepted", in)
		}
	}
	if !embed.Loopback("http://127.0.0.1:1") || !embed.Loopback("http://localhost:1") || !embed.Loopback("http://[::1]:1") || embed.Loopback("http://10.0.0.2:1") || embed.Loopback("http://emb.example") {
		t.Fatal("Loopback")
	}
}

func TestClientEmbedsQueriesAndDocuments(t *testing.T) {
	srv := embedtest.New(t)
	c, err := embed.New(embed.Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if c.Model() != embed.DefaultModel || c.PrefixVersion() != embed.PrefixVersion {
		t.Fatalf("model %q prefix %q", c.Model(), c.PrefixVersion())
	}
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	q, err := c.Query(context.Background(), "alpha beta")
	if err != nil {
		t.Fatal(err)
	}
	if got := srv.Requests(); len(got) != 1 || got[0][0] != "task: search result | query: alpha beta" {
		t.Fatalf("the server must receive the prefixed query: %q", got)
	}
	docs, err := c.Documents(context.Background(), []string{embed.DocumentText("a", "alpha beta"), embed.DocumentText("b", "gamma")})
	if err != nil || len(docs) != 2 {
		t.Fatalf("documents: %v %v", docs, err)
	}
	for _, v := range append(docs, q) {
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if math.Abs(sum-1) > 1e-5 {
			t.Fatalf("vector is not normalised: %f", sum)
		}
	}
	if cos(q, docs[0]) <= cos(q, docs[1]) {
		t.Fatal("a document with the query's words must be closer than one without")
	}
}

func cos(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func TestClientErrors(t *testing.T) {
	srv := embedtest.New(t)
	c, _ := embed.New(embed.Config{URL: srv.URL})

	srv.Fail.Store(true)
	_, err := c.Query(context.Background(), "x")
	var se *embed.StatusError
	if !errors.As(err, &se) || se.Code != 500 || se.Permanent() || embed.Permanent(err) {
		t.Fatalf("500 must be a transient status error: %v", err)
	}
	if !strings.Contains(err.Error(), "model crashed") {
		t.Fatalf("the server's message belongs in the error: %v", err)
	}
	srv.Fail.Store(false)

	srv.Reject.Store(10)
	_, err = c.Documents(context.Background(), []string{embed.DocumentText("t", strings.Repeat("x", 50))})
	if !embed.Permanent(err) {
		t.Fatalf("400 must be permanent: %v", err)
	}
	srv.Reject.Store(0)

	// A server that does not know the path, or wants a key, is a configuration
	// problem that can be cured, not a text that has to be written off.
	for _, code := range []int{401, 403, 404, 429, 503} {
		if embed.Permanent(&embed.StatusError{Code: code}) {
			t.Errorf("%d must not be permanent", code)
		}
	}
	for _, code := range []int{400, 413, 422} {
		if !embed.Permanent(&embed.StatusError{Code: code}) {
			t.Errorf("%d must be permanent", code)
		}
	}

	srv.Hang.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := c.Query(ctx, "x"); err == nil || embed.Permanent(err) {
		t.Fatalf("a hanging server must end in a transient error: %v", err)
	}
	srv.Hang.Store(false)

	srv.SetHealthy(false)
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("health must report a server that is still loading")
	}
}

func TestClientRejectsBrokenAnswers(t *testing.T) {
	answers := map[string]string{
		"count":    `{"data":[]}`,
		"empty":    `{"data":[{"index":0,"embedding":[]}]}`,
		"zero":     `{"data":[{"index":0,"embedding":[0,0,0]}]}`,
		"index":    `{"data":[{"index":3,"embedding":[1,2,3]}]}`,
		"not json": `<html>`,
	}
	for name, body := range answers {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer ts.Close()
			c, _ := embed.New(embed.Config{URL: ts.URL})
			if _, err := c.Query(context.Background(), "x"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// Vectors may come back in any order; "index" decides.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer ts.Close()
	c, _ := embed.New(embed.Config{URL: ts.URL})
	got, err := c.Documents(context.Background(), []string{"a", "b"})
	if err != nil || got[0][0] != 1 || got[1][1] != 1 {
		t.Fatalf("order by index: %v %v", got, err)
	}
}

func TestQuantizedCosineStaysCloseToFloat(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	rand32 := func() []float32 {
		v := make([]float32, 768)
		for i := range v {
			v[i] = float32(rng.NormFloat64())
		}
		if err := embed.Normalize(v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for range 50 {
		doc, query := rand32(), rand32()
		// Make the pair similar so the cosine is not near zero.
		for i := range doc {
			doc[i] = 0.6*doc[i] + 0.8*query[i]
		}
		if err := embed.Normalize(doc); err != nil {
			t.Fatal(err)
		}
		q, scale, err := embed.Quantize(doc)
		if err != nil || len(q) != 768 {
			t.Fatal(err)
		}
		if got, want := float64(embed.Cosine(query, q, scale)), cos(query, doc); math.Abs(got-want) > 0.01 {
			t.Fatalf("quantised cosine %f, float cosine %f", got, want)
		}
	}
	if _, _, err := embed.Quantize(nil); err == nil {
		t.Fatal("empty vector")
	}
	if _, _, err := embed.Quantize([]float32{0, 0}); err == nil {
		t.Fatal("zero vector")
	}
	if _, _, err := embed.Quantize([]float32{float32(math.NaN())}); err == nil {
		t.Fatal("NaN")
	}
	if err := embed.Normalize([]float32{float32(math.Inf(1)), 1}); err == nil {
		t.Fatal("Inf")
	}
}

func TestFuseIsReciprocalRankFusion(t *testing.T) {
	text := []int64{1, 2, 3}
	vector := []int64{9, 2, 1}
	got := embed.Fuse(text, vector)
	var ids []int64
	for _, f := range got {
		ids = append(ids, f.ID)
	}
	// 1: 1/61+1/63 = .032266, 2: 1/62+1/62 = .032258, 9: 1/61, 3: 1/63. An entry
	// in both lists beats one in a single list, and the sum of reciprocals, not
	// the average rank, decides between the two that are in both.
	want := []int64{1, 2, 9, 3}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("fused order %v, want %v", ids, want)
		}
	}
	if got[1].Ranks[0] != 2 || got[1].Ranks[1] != 2 {
		t.Fatalf("ranks %v", got[1].Ranks)
	}
	for _, f := range got {
		if f.ID == 9 && f.Ranks[0] != 0 {
			t.Fatalf("an entry the full-text list lacks has rank 0 there: %v", f.Ranks)
		}
	}
	exact := 1/float64(embed.RRFK+2) + 1/float64(embed.RRFK+2)
	if math.Abs(got[1].Score-exact) > 1e-12 {
		t.Fatalf("score %v, want %v", got[1].Score, exact)
	}
}

// A weight scales what a rank in a list is worth: the vectors at weight 2 count
// twice, and a weight that is missing or not positive counts once, so no weights
// are Fuse.
func TestFuseWeightedScalesTheRanksOfAList(t *testing.T) {
	text := []int64{1, 2}
	vector := []int64{2, 1}
	ids := func(fs []embed.Fused) []int64 {
		var out []int64
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	// Equal weights: 1 and 2 have the same score, the text list breaks the tie.
	if got := ids(embed.FuseWeighted([]float64{1, 1}, text, vector)); got[0] != 1 || got[1] != 2 {
		t.Fatalf("equal weights: %v", got)
	}
	// The vectors counting twice put their first, 2, on top: 1/62+2/61 against 1/61+2/62.
	fused := embed.FuseWeighted([]float64{1, 2}, text, vector)
	if got := ids(fused); got[0] != 2 || got[1] != 1 {
		t.Fatalf("weight 2 on the vectors: %v", got)
	}
	exact := 1/float64(embed.RRFK+2) + 2/float64(embed.RRFK+1)
	if math.Abs(fused[0].Score-exact) > 1e-12 {
		t.Fatalf("score %v, want %v", fused[0].Score, exact)
	}
	// Missing, zero and negative weights are 1; no weights at all are Fuse, to the bit.
	for _, w := range [][]float64{nil, {}, {1}, {0, 0}, {-3, -1}} {
		a, b := embed.FuseWeighted(w, text, vector), embed.Fuse(text, vector)
		for i := range a {
			if a[i].ID != b[i].ID || a[i].Score != b[i].Score {
				t.Fatalf("weights %v: %+v, want the fusion without weights %+v", w, a, b)
			}
		}
	}
	// A weight on a list the entry is not in adds nothing to it.
	if got := embed.FuseWeighted([]float64{1, 5}, []int64{7}, nil); len(got) != 1 || got[0].Score != 1/float64(embed.RRFK+1) {
		t.Fatalf("%+v", got)
	}
}

func TestFuseTiesFollowTheFirstList(t *testing.T) {
	// 5 is first in the text list only, 6 first in the vector list only: equal
	// scores, and the text list wins.
	got := embed.Fuse([]int64{5}, []int64{6})
	if got[0].ID != 5 || got[1].ID != 6 {
		t.Fatalf("%v", got)
	}
	if got := embed.Fuse(nil, []int64{4, 3}); got[0].ID != 4 || len(got) != 2 {
		t.Fatalf("a single list keeps its order: %v", got)
	}
	if len(embed.Fuse()) != 0 || len(embed.Fuse(nil, nil)) != 0 {
		t.Fatal("nothing in, nothing out")
	}
	if got := embed.Fuse([]int64{1, 1, 2}); len(got) != 2 || got[0].Ranks[0] != 1 {
		t.Fatalf("a repeated id keeps its best rank: %v", got)
	}
}
