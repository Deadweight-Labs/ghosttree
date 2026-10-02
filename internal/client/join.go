package client

import (
	"context"
	"encoding/json"
	"errors"
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
