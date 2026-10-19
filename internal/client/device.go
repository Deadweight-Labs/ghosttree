package client

import (
	"context"
	"encoding/json"
	"errors"
)

// DeviceAuth ist die Antwort auf den Start des Geräte-Logins (RFC 8628).
type DeviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceToken ist das ausgestellte Token.
type DeviceToken struct {
	AccessToken string `json:"access_token"`
	Machine     string `json:"machine"`
	TokenID     int64  `json:"token_id"`
}

// DevicePollError ist eine RFC-8628-Fehlerantwort des Token-Endpunkts, etwa
// authorization_pending oder slow_down. Interval ist das vom Server verlangte
// Abfrageintervall in Sekunden, 0 wenn er keines nannte.
type DevicePollError struct {
	Code     string
	Interval int
}

func (e *DevicePollError) Error() string { return e.Code }

// StartDeviceLogin beginnt den Geräte-Login. Der Aufruf ist unangemeldet und
// sendet kein Token.
func (c *Client) StartDeviceLogin(ctx context.Context, machine string) (DeviceAuth, error) {
	var out DeviceAuth
	err := c.anonymous().doContext(ctx, "POST", "/api/auth/device", nil, map[string]string{"machine": machine}, &out)
	return out, err
}

// PollDeviceLogin fragt das Token ab. Solange es nicht da ist, kommt ein
// *DevicePollError zurück.
func (c *Client) PollDeviceLogin(ctx context.Context, deviceCode string) (DeviceToken, error) {
	var out DeviceToken
	err := c.anonymous().doContext(ctx, "POST", "/api/auth/device/token", nil, map[string]string{"device_code": deviceCode}, &out)
	var se *StatusError
	if errors.As(err, &se) {
		var body struct {
			Error    string `json:"error"`
			Interval int    `json:"interval"`
		}
		if json.Unmarshal([]byte(se.Body), &body) == nil && body.Error != "" {
			return DeviceToken{}, &DevicePollError{Code: body.Error, Interval: body.Interval}
		}
	}
	return out, err
}

// anonymous gibt einen Client ohne Token zurück: die Endpunkte des Geräte-Logins
// brauchen keines, und ein altes, ungültiges soll nicht mitgeschickt werden.
func (c *Client) anonymous() *Client {
	cfg := c.cfg
	cfg.Token = ""
	return &Client{cfg: cfg, http: c.http}
}
