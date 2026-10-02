package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

// JoinClaimRequest ist der Körper von POST /api/join/claim. Die drei
// Loopback-Felder (Challenge, Port, State) stehen zusammen oder gar nicht.
type JoinClaimRequest struct {
	Pair                string `json:"pair"`
	Machine             string `json:"machine"`
	CodeChallenge       string `json:"code_challenge,omitempty"`
	CodeChallengeMethod string `json:"code_challenge_method,omitempty"`
	LoopbackPort        int    `json:"loopback_port,omitempty"`
	LoopbackHost        string `json:"loopback_host,omitempty"`
	State               string `json:"state,omitempty"`
}

// JoinClaim ist die Antwort auf einen Claim: Mode "loopback" oder "code".
type JoinClaim struct {
	Mode          string `json:"mode"`
	ExpiresIn     int    `json:"expires_in"`
	DeviceCode    string `json:"device_code"`
	ConfirmCode   string `json:"confirm_code"`
	Interval      int    `json:"interval"`
	TokenEndpoint string `json:"token_endpoint"`
}

// JoinClaim meldet dieses Gerät mit dem Paarungscode an. Der Aufruf ist
// unangemeldet.
func (c *Client) JoinClaim(ctx context.Context, req JoinClaimRequest) (JoinClaim, error) {
	var out JoinClaim
	err := c.anonymous().doContext(ctx, "POST", "/api/join/claim", nil, req, &out)
	return out, err
}

// JoinExchange tauscht den Code vom Loopback-Callback gegen das Token.
func (c *Client) JoinExchange(ctx context.Context, code, verifier string) (DeviceToken, error) {
	var out DeviceToken
	err := c.anonymous().doContext(ctx, "POST", "/api/join/token", nil,
		map[string]string{"code": code, "code_verifier": verifier}, &out)
	return out, err
}

// ErrorCode liefert das Feld "error" einer OAuth-artigen Fehlerantwort
// (invalid_pair, invalid_grant, machine_name_taken, too_many_requests) oder "".
func ErrorCode(err error) string {
	var se *StatusError
	if !errors.As(err, &se) {
		return ""
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(se.Body), &body) != nil {
		return ""
	}
	return body.Error
}

// RevokeSelf widerruft das Token dieses Clients und gibt seine Maschine frei.
func (c *Client) RevokeSelf(ctx context.Context) error {
	return c.doContext(ctx, "DELETE", "/api/tokens/self", nil, nil, nil)
}

// WhoAmIContext ist WhoAmI mit Kontext, damit ein Abbruch die Anfrage beendet.
func (c *Client) WhoAmIContext(ctx context.Context) (store.Principal, error) {
	var principal store.Principal
	err := c.doContext(ctx, "GET", "/api/whoami", nil, nil, &principal)
	return principal, err
}

// ListOrgsContext ist ListOrgs mit Kontext.
func (c *Client) ListOrgsContext(ctx context.Context) ([]store.Org, error) {
	var out []store.Org
	err := c.doContext(ctx, "GET", "/api/orgs", nil, nil, &out)
	return out, err
}
