package netguard

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// KN-4825 m3: the client for pages speaks only to the ports 80 and 443 of a
// public host; a found address with another port (a mail server on 25) is
// refused before anything is dialled. The address is a literal, so nothing
// is resolved and nothing leaves the machine.
func TestPublicClientRefusesPortsOtherThanTheWebs(t *testing.T) {
	c := PublicClient(2*time.Second, 3)
	for _, u := range []string{"http://1.1.1.1:25/", "https://1.1.1.1:8443/", "http://1.1.1.1:6379/x"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		_, err := c.Do(req)
		cancel()
		if !errors.Is(err, ErrPort) {
			t.Errorf("%s: %v", u, err)
		}
	}
	// A host that is not public is named as such, whatever its port.
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:25/", nil)
	if _, err := c.Do(req); !errors.Is(err, ErrNotPublic) {
		t.Errorf("loopback: %v", err)
	}
	// The dialler of the connections keeps every port.
	if _, err := PublicDialer(time.Second)(context.Background(), "tcp", "127.0.0.1:8443"); !errors.Is(err, ErrNotPublic) {
		t.Errorf("connections dialler: %v", err)
	}
}
