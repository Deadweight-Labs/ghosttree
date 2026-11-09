package web

import (
	"net/http"
	"testing"
)

// followRefusal opens the page a refused form was sent back to. The test jar keeps no
// cookies, so the cookie of the answer (the note and the typed text, once) goes along by hand.
func followRefusal(t *testing.T, c *http.Client, base string, resp *http.Response) (int, string) {
	t.Helper()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || loc == "" {
		t.Fatalf("the answer %d is no redirect to the form", resp.StatusCode)
	}
	req, err := http.NewRequest(http.MethodGet, base+loc, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == flashCookie {
			req.AddCookie(ck)
		}
	}
	got, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	return got.StatusCode, body(t, got)
}

type navigationTransport struct{ base http.RoundTripper }

func (n navigationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	base := n.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// asBrowser is the client as a browser sends a form: as a change of page. Only then does a
// refused form come back to its page (formNavigation); the other tests keep the plain answers.
func asBrowser(c *http.Client) *http.Client {
	copied := *c
	copied.Transport = navigationTransport{base: c.Transport}
	return &copied
}
