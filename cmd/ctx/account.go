package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

const accountUsage = `usage: ctx account add <name> [--email E] [--admin] --db <path>
       ctx account list --db <path>
       ctx account tokens <name> --db <path>
       ctx account token create <name> [--label L] [--machine M] [--expires-in 720h] --db <path>
       ctx account token revoke <id> --db <path>
       ctx account claim-code <name> --db <path>
       ctx account login-link <name> [--url https://host] --db <path>`

// cmdAccount arbeitet wie person direkt auf der Datenbank: Konten und Tokens
// auszustellen braucht Zugriff auf den Server-Host, nicht ein Token.
func cmdAccount(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, accountUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return cmdAccountAdd(args[1:], stdout)
	case "list":
		return cmdAccountList(args[1:], stdout)
	case "tokens":
		return cmdAccountTokens(args[1:], stdout)
	case "claim-code":
		return cmdAccountClaimCode(args[1:], stdout)
	case "login-link":
		return cmdAccountLoginLink(args[1:], stdout)
	case "token":
		if len(args) > 1 && args[1] == "create" {
			return cmdAccountTokenCreate(args[2:], stdout)
		}
		if len(args) > 1 && args[1] == "revoke" {
			return cmdAccountTokenRevoke(args[2:], stdout)
		}
	}
	fmt.Fprintln(stdout, accountUsage)
	return 2
}

// accountFlags parst ein optionales führendes Positionsargument vor den Flags.
func accountFlags(name string, args []string, stdout io.Writer, setup func(*flag.FlagSet)) (*flag.FlagSet, string, *string, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stdout)
	db := fs.String("db", "ghosttree.db", "path to the sqlite database")
	if setup != nil {
		setup(fs)
	}
	var positional string
	rest := args
	if len(rest) > 0 && rest[0] != "" && rest[0][0] != '-' {
		positional, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
		return fs, "", db, false
	}
	return fs, positional, db, true
}

func openAccountStore(db string, stdout io.Writer) (*store.Store, bool) {
	st, err := store.Open(db)
	if err != nil {
		fmt.Fprintf(stdout, "open db: %v\n", err)
		return nil, false
	}
	return st, true
}

func cmdAccountAdd(args []string, stdout io.Writer) int {
	var email *string
	var admin *bool
	_, name, db, ok := accountFlags("account add", args, stdout, func(fs *flag.FlagSet) {
		email = fs.String("email", "", "email for display and invitation matching")
		admin = fs.Bool("admin", false, "make the account an instance admin")
	})
	if !ok || name == "" {
		fmt.Fprintln(stdout, "usage: ctx account add <name> [--email E] [--admin] --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	a, err := st.AddAccount(name, *email, *admin)
	if err != nil {
		fmt.Fprintf(stdout, "add account: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "account %s\t%s\n", a.ID, a.Name)
	fmt.Fprintln(stdout, "no token yet: ctx account token create "+a.Name)
	return 0
}

func cmdAccountList(args []string, stdout io.Writer) int {
	_, _, db, ok := accountFlags("account list", args, stdout, nil)
	if !ok {
		fmt.Fprintln(stdout, "usage: ctx account list --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	accounts, err := st.ListAccounts()
	if err != nil {
		fmt.Fprintf(stdout, "list accounts: %v\n", err)
		return 1
	}
	for _, a := range accounts {
		fmt.Fprintf(stdout, "%s\t%s\tstate=%s admin=%t email=%s\n", a.ID, a.Name, a.State, a.Admin, a.Email)
	}
	return 0
}

func cmdAccountTokens(args []string, stdout io.Writer) int {
	_, name, db, ok := accountFlags("account tokens", args, stdout, nil)
	if !ok || name == "" {
		fmt.Fprintln(stdout, "usage: ctx account tokens <name> --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	tokens, err := st.ListTokens(name)
	if err != nil {
		fmt.Fprintf(stdout, "list tokens: %v\n", err)
		return 1
	}
	for _, t := range tokens {
		status := "active"
		switch {
		case t.RevokedAt != "":
			status = "revoked"
		case t.ExpiresAt != "" && t.ExpiresAt <= time.Now().UTC().Format(time.RFC3339):
			status = "expired"
		}
		fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\tmachine=%s created=%s expires=%s\n",
			t.ID, t.Kind, t.Label, status, t.Machine, t.CreatedAt, t.ExpiresAt)
	}
	return 0
}

func cmdAccountTokenCreate(args []string, stdout io.Writer) int {
	var label, machine *string
	var expires *time.Duration
	_, name, db, ok := accountFlags("account token create", args, stdout, func(fs *flag.FlagSet) {
		label = fs.String("label", "", "what the token is for")
		machine = fs.String("machine", "", "machine name the token is bound to")
		expires = fs.Duration("expires-in", 0, "lifetime, e.g. 720h (default: does not expire)")
	})
	if !ok || name == "" {
		fmt.Fprintln(stdout, "usage: ctx account token create <name> [--label L] [--machine M] [--expires-in 720h] --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	token, info, err := st.CreateToken(name, store.TokenSpec{Label: *label, Machine: *machine, ExpiresIn: *expires})
	if err != nil {
		fmt.Fprintf(stdout, "create token: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "token: %s\n", token)
	fmt.Fprintf(stdout, "id: %d\n", info.ID)
	fmt.Fprintln(stdout, "this token is shown once, store it now")
	return 0
}

func cmdAccountTokenRevoke(args []string, stdout io.Writer) int {
	_, raw, db, ok := accountFlags("account token revoke", args, stdout, nil)
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil {
		fmt.Fprintln(stdout, "usage: ctx account token revoke <id> --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.RevokeToken(id); err != nil {
		fmt.Fprintf(stdout, "revoke token: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "token %d revoked\n", id)
	return 0
}

func cmdAccountClaimCode(args []string, stdout io.Writer) int {
	_, name, db, ok := accountFlags("account claim-code", args, stdout, nil)
	if !ok || name == "" {
		fmt.Fprintln(stdout, "usage: ctx account claim-code <name> --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	code, ttl, err := st.CreateAccountCode(store.CodeClaim, name)
	if err != nil {
		fmt.Fprintf(stdout, "claim code: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "claim code: %s\n", code)
	fmt.Fprintf(stdout, "valid %s, single use. Paste it into the code field on the OIDC sign-in page; the first login that presents it is bound to %s.\n", ttl, name)
	return 0
}

func cmdAccountLoginLink(args []string, stdout io.Writer) int {
	var base *string
	_, name, db, ok := accountFlags("account login-link", args, stdout, func(fs *flag.FlagSet) {
		base = fs.String("url", "http://127.0.0.1:8474", "public base URL of the server")
	})
	if !ok || name == "" {
		fmt.Fprintln(stdout, "usage: ctx account login-link <name> [--url https://host] --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	code, ttl, err := st.CreateAccountCode(store.CodeLogin, name)
	if err != nil {
		fmt.Fprintf(stdout, "login link: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s/ui/login/code?code=%s\n", strings.TrimRight(*base, "/"), code)
	fmt.Fprintf(stdout, "valid %s, single use\n", ttl)
	return 0
}
