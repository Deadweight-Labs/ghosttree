package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
)

// loginSleep wartet zwischen zwei Abfragen; Tests ersetzen es.
var loginSleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cmdLogin meldet diese Maschine über den Geräte-Login am Server an: die CLI
// zeigt URL und Code, der Mensch bestätigt im Browser, die CLI holt das Token
// und schreibt es in die Client-Konfiguration (dieselbe Datei und dieselben
// Rechte wie ctx setup).
func cmdLogin(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stdout)
	serverURL := fs.String("server", "", "ghosttree server URL (default: the one in the client config)")
	machine := fs.String("machine", "", "machine name (default: hostname)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, "usage: ctx login [--server <url>] [--machine <name>]")
		return 2
	}
	existing, _ := config.Load()
	cfg := config.Config{ServerURL: strings.TrimSpace(*serverURL), Machine: *machine}
	if cfg.ServerURL == "" {
		cfg.ServerURL = existing.ServerURL
	}
	if cfg.ServerURL == "" {
		fmt.Fprintln(stdout, "no server: pass --server <url> or run ctx setup first")
		return 2
	}
	if cfg.Machine == "" {
		cfg.Machine = existing.Machine
	}
	if cfg.Machine == "" {
		cfg.Machine, _ = os.Hostname()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token, err := deviceLogin(ctx, client.New(cfg), cfg.Machine, stdout)
	if err != nil {
		fmt.Fprintf(stdout, "login failed: %v\n", err)
		return 1
	}
	cfg.Token = token.AccessToken
	if token.Machine != "" {
		cfg.Machine = token.Machine
	}
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(stdout, "save config: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in; wrote %s (machine %s)\n", config.Path(), cfg.Machine)
	if who, err := client.New(cfg).WhoAmI(); err == nil {
		fmt.Fprintf(stdout, "signed in as %s\n", who.Label)
	}
	return 0
}

func deviceLogin(ctx context.Context, c *client.Client, machine string, stdout io.Writer) (client.DeviceToken, error) {
	auth, err := c.StartDeviceLogin(ctx, machine)
	if err != nil {
		return client.DeviceToken{}, err
	}
	fmt.Fprintf(stdout, "Open %s and enter the code:\n\n    %s\n\n", auth.VerificationURI, auth.UserCode)
	fmt.Fprintf(stdout, "Or open %s directly. Waiting for approval (expires in %d minutes)...\n", auth.VerificationURIComplete, (auth.ExpiresIn+59)/60)
	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		if err := loginSleep(ctx, interval); err != nil {
			return client.DeviceToken{}, err
		}
		token, err := c.PollDeviceLogin(ctx, auth.DeviceCode)
		if err == nil {
			return token, nil
		}
		var pe *client.DevicePollError
		if !errors.As(err, &pe) {
			return client.DeviceToken{}, err
		}
		switch pe.Code {
		case "authorization_pending":
		case "slow_down":
			if pe.Interval > 0 {
				interval = time.Duration(pe.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
		case "access_denied":
			return client.DeviceToken{}, errors.New("the login was denied in the browser")
		case "expired_token":
			return client.DeviceToken{}, errors.New("the code expired; run ctx login again")
		default:
			return client.DeviceToken{}, fmt.Errorf("server answered %q", pe.Code)
		}
	}
	return client.DeviceToken{}, errors.New("timed out waiting for approval")
}
