package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
	"github.com/Deadweight-Labs/ghosttree/internal/tui"
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
	plain := fs.Bool("plain", false, "print URL and code instead of opening the terminal interface")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stdout, "usage: ctx login [--server <url>] [--machine <name>] [--plain]")
		return 2
	}
	if !*plain && interactiveTerminal(stdout) {
		return runTUILogin(strings.TrimSpace(*serverURL), *machine, stdout)
	}
	previous, _ := config.Load()
	cfg, err := loginConfig(*serverURL, *machine)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token, err := deviceLogin(ctx, client.New(cfg), cfg.Machine, stdout)
	if err != nil {
		var taken *machineTakenError
		if errors.As(err, &taken) {
			fmt.Fprint(stdout, loginTakenMessage(cfg.ServerURL, cfg.Machine))
			return 1
		}
		fmt.Fprintf(stdout, "login failed: %s\n", links.Clean(err.Error(), 300))
		return 1
	}
	cfg, err = saveLogin(cfg, token)
	if err != nil {
		fmt.Fprintf(stdout, "save config: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in; wrote %s (machine %s)\n", config.Path(), cfg.Machine)
	if who, err := client.New(cfg).WhoAmI(); err == nil {
		fmt.Fprintf(stdout, "signed in as %s\n", links.Clean(who.Label, 60))
	}
	note, warning := retireOldLogin(ctx, previous, cfg)
	if note != "" {
		fmt.Fprintln(stdout, note)
	}
	if warning != "" {
		fmt.Fprintf(stdout, "warning: %s\n", warning)
	}
	return 0
}

// loginConfig is the config a login writes to: the given server and machine,
// else the ones already configured, else the hostname. A new login keeps the
// update channel.
func loginConfig(serverURL, machine string) (config.Config, error) {
	existing, _ := config.Load()
	cfg := config.Config{ServerURL: strings.TrimSpace(serverURL), Machine: strings.TrimSpace(machine), Channel: existing.Channel}
	if cfg.ServerURL == "" {
		cfg.ServerURL = existing.ServerURL
	}
	if cfg.ServerURL == "" {
		return cfg, errors.New("no server: pass --server <url> or run ctx setup first")
	}
	if cfg.Machine == "" {
		cfg.Machine = existing.Machine
	}
	if cfg.Machine == "" {
		cfg.Machine, _ = os.Hostname()
	}
	return cfg, nil
}

// saveLogin stores the token of a finished login.
func saveLogin(cfg config.Config, token client.DeviceToken) (config.Config, error) {
	cfg.Token = token.AccessToken
	if m := links.Clean(token.Machine, 64); m != "" {
		cfg.Machine = m
	}
	return cfg, config.Save(cfg)
}

// retireOldLogin revokes the token of the previous server after a login on
// another one, because the client config only keeps one and the old token
// would otherwise stay valid with nobody holding it. Its result is a note on
// success and a warning when the old server could not confirm it. Signing in
// again on the same server revokes nothing: that would free the machine name
// the new login just took.
func retireOldLogin(ctx context.Context, previous, now config.Config) (note, warning string) {
	if previous.Token == "" || previous.ServerURL == "" || sameServer(previous.ServerURL, now.ServerURL) {
		return "", ""
	}
	old := links.Clean(previous.ServerURL, 200)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := client.NewWithTimeout(previous, 10*time.Second).RevokeSelf(ctx)
	var se *client.StatusError
	if err == nil || (errors.As(err, &se) && se.Status == http.StatusUnauthorized) {
		return "signed out of " + old, ""
	}
	return "", "the login on " + old + " could not be revoked (" + links.Clean(err.Error(), 160) + "); revoke this device in its web interface under account, devices"
}

func sameServer(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

func deviceLogin(ctx context.Context, c *client.Client, machine string, stdout io.Writer) (client.DeviceToken, error) {
	auth, err := c.StartDeviceLogin(ctx, machine)
	if err != nil {
		return client.DeviceToken{}, err
	}
	fmt.Fprintf(stdout, "Open %s and enter the code:\n\n    %s\n\n", links.Clean(auth.VerificationURI, 2048), tui.LoginCode(auth.UserCode))
	fmt.Fprintf(stdout, "Or open %s directly. Waiting for approval (expires in %d minutes)...\n", links.Clean(auth.VerificationURIComplete, 2048), (auth.ExpiresIn+59)/60)
	return pollDeviceToken(ctx, c, auth)
}

// pollDeviceToken waits until the person approved the login in the browser.
func pollDeviceToken(ctx context.Context, c *client.Client, auth client.DeviceAuth) (client.DeviceToken, error) {
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
		case "machine_name_taken":
			return client.DeviceToken{}, &machineTakenError{}
		case "expired_token":
			return client.DeviceToken{}, errors.New("the code expired; run ctx login again")
		default:
			return client.DeviceToken{}, fmt.Errorf("server answered %q", pe.Code)
		}
	}
	return client.DeviceToken{}, errors.New("timed out waiting for approval")
}

// loginTakenMessage says why the login stopped and names the exact command to
// run again, with another machine name (as ctx join does for a taken name).
func loginTakenMessage(server, machine string) string {
	suggestion := machineSuggestion(machine)
	return fmt.Sprintf("The machine name %s already belongs to another account.\nRun this with a name of your choice:\n\n  %s login --server %s --machine %s\n",
		safe(machine), ctxHint(), server, suggestion)
}

// machineSuggestion is another machine name to try when one is taken.
func machineSuggestion(machine string) string {
	suggestion := machine + "-2"
	if len(suggestion) > 64 {
		suggestion = "my-" + machine[:60]
	}
	return suggestion
}
