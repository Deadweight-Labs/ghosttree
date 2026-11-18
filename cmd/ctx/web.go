package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/client"
	"github.com/Deadweight-Labs/ghosttree/internal/config"
	"github.com/Deadweight-Labs/ghosttree/internal/links"
)

const webUsage = `usage: ctx web [--print] [--json]

Signs you in to the web interface from this machine, without a password or a
mail: the server makes a one-time link for your account, tied to this machine's
token. The link works once, for two minutes; opening it asks you to confirm the
sign-in. A browser opens on this machine; over ssh or without a display the link
is only printed. A revoked token makes an unused link worthless.

--print never opens a browser. --json prints {"url":…,"expires_in":…}.`

func cmdWeb(args []string, stdout io.Writer) int {
	args, jsonMode := removeBoolArg(args, "--json")
	args, printOnly := removeBoolArg(args, "--print")
	if len(args) > 0 {
		fmt.Fprintln(stdout, webUsage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil || cfg.Token == "" {
		return writeRequestCLIError(stdout, jsonMode, 1, fmt.Errorf("this machine is not connected: run ctx login first"))
	}
	code, expires, err := client.New(cfg).WebLogin()
	if err != nil {
		return writeRequestCLIError(stdout, jsonMode, 1, err)
	}
	link := strings.TrimSuffix(cfg.ServerURL, "/") + "/ui/login/code?code=" + code
	opened, why := false, ""
	if printOnly {
		why = "--print"
	} else if ok, reason := browserAllowed(); !ok {
		why = reason
	} else if err := openLink(link); err != nil {
		why = "could not start the browser: " + links.Clean(err.Error(), 120)
	} else {
		opened = true
	}
	if jsonMode {
		out, _ := json.Marshal(map[string]any{"url": link, "expires_in": expires, "opened": opened})
		fmt.Fprintln(stdout, string(out))
		return 0
	}
	if opened {
		fmt.Fprintf(stdout, "opened the sign-in in your browser; the link works once, for %s\n", (time.Duration(expires) * time.Second).String())
		return 0
	}
	fmt.Fprintln(stdout, link)
	fmt.Fprintf(stdout, "open this link to sign in (%s); it works once, for %s\n", why, (time.Duration(expires) * time.Second).String())
	return 0
}
