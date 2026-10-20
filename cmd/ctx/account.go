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
       ctx account login-link <name> [--url https://host] --db <path>
       ctx account set-gardener <name|person:N|N> --db <path>
       ctx account set-gardener --clear --db <path>

set-gardener makes one account the gardener's, once and for good: the number of
the account is stored (name, or person:N, or the bare number is looked up once),
not its name, so renaming that account or giving its old name to another does not
change who the gardener is. Only that account may write entries with harness
"gardener" or a "gardener:" reference, and the review page acts only on notes it
wrote. The running server picks it up at once. Without one nobody may, and the
gardener's proposals are refused. Give it the role member in the project, not more.`

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
	case "set-gardener":
		return cmdAccountSetGardener(args[1:], stdout)
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
	fmt.Fprintln(stdout, "no token yet: ctx account token create "+shellQuote(a.Name))
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
	gardener := st.GardenerAccountID()
	for _, a := range accounts {
		fmt.Fprintf(stdout, "%s\t%s\tstate=%s admin=%t email=%s", a.ID, a.Name, a.State, a.Admin, a.Email)
		if id, ok := store.AccountNumericID(a.ID); ok && id == gardener {
			fmt.Fprint(stdout, " gardener=true")
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

// cmdAccountSetGardener names the account the gardener runs as. Like the other
// account commands it works on the database itself: whoever has the host decides
// who the gardener is, and nothing a user can do over HTTP changes it.
func cmdAccountSetGardener(args []string, stdout io.Writer) int {
	var unset *bool
	_, who, db, ok := accountFlags("account set-gardener", args, stdout, func(fs *flag.FlagSet) {
		unset = fs.Bool("clear", false, "take the role away: nobody is the gardener afterwards")
	})
	if !ok || (who == "") == !*unset {
		fmt.Fprintln(stdout, "usage: ctx account set-gardener <name|person:N|N> --db <path>\n       ctx account set-gardener --clear --db <path>")
		return 2
	}
	st, ok := openAccountStore(*db, stdout)
	if !ok {
		return 1
	}
	defer st.Close()
	before, wasSet := st.GardenerAccount()
	if *unset {
		if err := st.ClearGardenerAccount(); err != nil {
			fmt.Fprintf(stdout, "clear gardener account: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "no gardener account is set: nobody may write gardener entries, and the gardener's proposals are refused")
		return 0
	}
	acct, err := resolveAccountRef(st, who)
	if err != nil {
		fmt.Fprintf(stdout, "%v\n", err)
		return 1
	}
	id, _ := store.AccountNumericID(acct.ID)
	if err := st.SetGardenerAccount(id); err != nil {
		fmt.Fprintf(stdout, "set gardener account: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "the gardener's account is %s (%s); the number is what counts, a rename changes nothing. The server picks it up at once.\n", acct.ID, acct.Name)
	if acct.Admin {
		fmt.Fprintln(stdout, "note: this account is an administrator; the gardener needs the role member in the project and no more")
	}
	if wasSet && before.ID != acct.ID {
		fmt.Fprintf(stdout, "it was %s (%s); that account may not write gardener entries any more\n", before.ID, before.Name)
	}
	return 0
}

// resolveAccountRef finds the account a person named on the command line: a
// number ("7" or "person:7") by its number, anything else by its name. The
// number is what the caller stores.
func resolveAccountRef(st *store.Store, ref string) (store.Account, error) {
	ref = strings.TrimSpace(ref)
	byNumber := ref
	if _, err := strconv.ParseInt(ref, 10, 64); err == nil {
		byNumber = "person:" + ref
	}
	if strings.HasPrefix(byNumber, "person:") {
		a, err := st.AccountByPrincipalID(byNumber)
		if err != nil {
			return store.Account{}, fmt.Errorf("no account %s", byNumber)
		}
		return a, nil
	}
	a, err := st.AccountByName(store.NormalizeAccountName(ref))
	if err != nil {
		return store.Account{}, fmt.Errorf("no account named %q (ctx account list shows them)", ref)
	}
	return a, nil
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

// shellQuote setzt ein Argument in Hochkommas, damit ein Name mit Leerzeichen
// oder Anführungszeichen im ausgegebenen Hinweis als eines gilt.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
