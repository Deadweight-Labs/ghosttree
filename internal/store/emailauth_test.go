package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// emailFixture: eine Instanz mit einem Owner (robin) und seiner Organisation.
func emailFixture(t *testing.T) (s *Store, owner Account, org Org) {
	t.Helper()
	s = openCodesStore(t)
	code, _, _ := s.EnsureBootstrapCode()
	owner, err := s.BootstrapLocal(code, "robin")
	if err != nil {
		t.Fatal(err)
	}
	orgs, _ := s.ListOrgs(owner.ID)
	if len(orgs) == 0 {
		t.Fatal("no org")
	}
	return s, owner, orgs[0]
}

func memberWithEmail(t *testing.T, s *Store, owner Account, org Org, name, email string) Account {
	t.Helper()
	code, _, err := s.CreateInvitation(owner.ID, org.ID, "", OrgMember, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.InviteLocal(code, name)
	if err != nil {
		t.Fatal(err)
	}
	if email != "" {
		c, err := s.IssueConfirmChallenge(a.ID, email)
		if err != nil {
			t.Fatal(err)
		}
		if a, err = s.RedeemChallenge(Redemption{Purpose: ChallengeConfirm, Link: c.Link}); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func TestLoginChallengeCodeLogsInOnceAndStoresOnlyHashes(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "Jonas@Example.test")

	c, ok, err := s.IssueLoginChallenge(ChallengeLogin, " jonas@example.TEST ")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(c.Code) != 8 || strings.ContainsAny(c.Code, "01IO") {
		t.Fatalf("code %q is not 8 characters without look-alikes", c.Code)
	}
	var codeHash, linkHash string
	if err := s.db.QueryRow(`SELECT code_hash, link_hash FROM email_challenges WHERE purpose='login'`).Scan(&codeHash, &linkHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(codeHash+linkHash, c.Code) || strings.Contains(codeHash+linkHash, c.Link) {
		t.Fatal("plaintext in the database")
	}
	// Eine falsche Eingabe, dann die richtige (mit Trennstrich und Kleinbuchstaben getippt).
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "jonas@example.test", Code: "AAAAAAAA"}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	typed := strings.ToLower(FormatCode(c.Code))
	a, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "jonas@example.test", Code: typed})
	if err != nil || a.ID != jonas.ID {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	// Einmalig, auch der Link der selben Mail ist damit tot.
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "jonas@example.test", Code: c.Code}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("code reused: %v", err)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Link: c.Link}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("link works after the code: %v", err)
	}
}

func TestLoginChallengeLinkIsPurposeBoundAndSingleUse(t *testing.T) {
	s, owner, org := emailFixture(t)
	memberWithEmail(t, s, owner, org, "jonas", "jonas@example.test")
	c, _, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test")
	if email, ok := s.PeekChallenge(ChallengeLogin, c.Link); !ok || email != "jonas@example.test" {
		t.Fatalf("peek: %q %v", email, ok)
	}
	// Das Ansehen verbraucht nichts (Mail-Vorschauen rufen Links ab).
	if _, ok := s.PeekChallenge(ChallengeLogin, c.Link); !ok {
		t.Fatal("peek used the link up")
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeReset, Link: c.Link, PasswordHash: "x"}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("a login link works as reset: %v", err)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Link: c.Link}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Link: c.Link}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("link reused: %v", err)
	}
}

func TestUnknownAddressGivesNoChallengeAndNoError(t *testing.T) {
	s, owner, org := emailFixture(t)
	memberWithEmail(t, s, owner, org, "jonas", "jonas@example.test")
	c, ok, err := s.IssueLoginChallenge(ChallengeLogin, "nobody@example.test")
	if err != nil || ok || c.Code != "" {
		t.Fatalf("c=%+v ok=%v err=%v", c, ok, err)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "nobody@example.test", Code: "ABCD2345"}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("unknown address: %v", err)
	}
	if _, _, err := s.IssueLoginChallenge(ChallengeLogin, "not an address"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad address: %v", err)
	}
}

func TestCodeLocksAfterFiveWrongTriesAndLooksTheSame(t *testing.T) {
	s, owner, org := emailFixture(t)
	memberWithEmail(t, s, owner, org, "jonas", "jonas@example.test")
	c, _, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test")
	for i := 0; i < 5; i++ {
		if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "jonas@example.test", Code: "WRONG222"}); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	// Auch der richtige Code geht jetzt nicht mehr; die Antwort ist dieselbe wie bei jedem Fehler.
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "jonas@example.test", Code: c.Code}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("locked challenge accepted the code: %v", err)
	}
}

func TestChallengeExpiresAndNewMailReplacesOld(t *testing.T) {
	s, owner, org := emailFixture(t)
	memberWithEmail(t, s, owner, org, "jonas", "jonas@example.test")
	old, _, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test")
	fresh, _, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test")
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Link: old.Link}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("older mail still works: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE email_challenges SET expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Email: "jonas@example.test", Code: fresh.Code}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired code works: %v", err)
	}
	if time.Until(fresh.ExpiresAt) > LoginChallengeTTL {
		t.Fatalf("login mail valid for %v, want a few minutes", time.Until(fresh.ExpiresAt))
	}
}

func TestUnconfirmedAddressDoesNotLogIn(t *testing.T) {
	s, owner, org := emailFixture(t)
	a := memberWithEmail(t, s, owner, org, "jonas", "")
	c, err := s.IssueConfirmChallenge(a.ID, "jonas@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.PendingEmail(a.ID); got != "jonas@example.test" {
		t.Fatalf("pending = %q", got)
	}
	if _, ok, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test"); ok {
		t.Fatal("an unconfirmed address got a sign-in mail")
	}
	if got, _ := s.AccountByPrincipalID(a.ID); got.Email != "" {
		t.Fatalf("email set before confirmation: %q", got.Email)
	}
	confirmed, err := s.RedeemChallenge(Redemption{Purpose: ChallengeConfirm, Email: "jonas@example.test", Code: c.Code})
	if err != nil || confirmed.Email != "jonas@example.test" {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	if got := s.PendingEmail(a.ID); got != "" {
		t.Fatalf("still pending: %q", got)
	}
	if _, ok, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test"); !ok {
		t.Fatal("a confirmed address gets no sign-in mail")
	}
}

func TestAddressOfAnotherAccountCannotBeConfirmed(t *testing.T) {
	s, owner, org := emailFixture(t)
	memberWithEmail(t, s, owner, org, "jonas", "shared@example.test")
	pia := memberWithEmail(t, s, owner, org, "pia", "")
	c, _ := s.IssueConfirmChallenge(pia.ID, "Shared@Example.test")
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeConfirm, Link: c.Link}); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("err = %v", err)
	}
	// Der Fehlschlag verbrauchte die Mail nicht, aber der Besitzer ändert sich nicht.
	if got, _ := s.AccountByPrincipalID(pia.ID); got.Email != "" {
		t.Fatalf("pia got %q", got.Email)
	}
}

func TestChangedAddressInvalidatesOldSignInMail(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "alt@example.test")
	old, _, _ := s.IssueLoginChallenge(ChallengeLogin, "alt@example.test")
	c, _ := s.IssueConfirmChallenge(jonas.ID, "neu@example.test")
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeConfirm, Link: c.Link}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeLogin, Link: old.Link}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("mail to the old address still signs in: %v", err)
	}
}

func TestTwoAccountsWithOneAddressGetNoMail(t *testing.T) {
	s, owner, org := emailFixture(t)
	a := memberWithEmail(t, s, owner, org, "a", "dup@example.test")
	b := memberWithEmail(t, s, owner, org, "b", "")
	// Altbestand: dieselbe Adresse zweimal, am Konto gesetzt statt bestätigt.
	if _, err := s.db.Exec(`UPDATE persons SET email='DUP@example.test' WHERE id=?`, mustID(t, b.ID)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.IssueLoginChallenge(ChallengeLogin, "dup@example.test"); ok {
		t.Fatalf("ambiguous address (%s, %s) got a mail", a.Name, b.Name)
	}
}

func mustID(t *testing.T, principal string) int64 {
	t.Helper()
	id, err := parsePersonPrincipalID(principal)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestJoinBoundInvitationNeedsTheMailBeforeAnythingExists(t *testing.T) {
	s, owner, org := emailFixture(t)
	code, _, err := s.CreateInvitation(owner.ID, org.ID, "Pia@Example.test", OrgMember, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Ohne Mail-Möglichkeit ist sie nicht einlösbar; die Vorschau sagt es.
	if _, err := s.PreviewJoin(code, false, false); !errors.Is(err, ErrInvitationEmail) {
		t.Fatalf("preview without mail: %v", err)
	}
	f, err := s.PreviewJoin(code, false, true)
	if err != nil || f.BoundEmail != "pia@example.test" || f.Inviter == "" {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	// Ein Name, den schon jemand trägt, wird gleich abgelehnt, vor jeder Mail.
	if _, _, err := s.BeginJoinByMail(code, "robin", "egal@example.test"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("taken name: %v", err)
	}
	a, c, err := s.BeginJoinByMail(code, "pia", "ignored@example.test")
	if err != nil || a.ID != "" || c.Email != "pia@example.test" || c.Purpose != ChallengeJoin {
		t.Fatalf("a=%+v c=%+v err=%v", a, c, err)
	}
	if _, err := s.AccountByName("pia"); err == nil {
		t.Fatal("the account exists before the mail was opened")
	}
	// Das Einlösen legt Konto, bestätigte Adresse und Mitgliedschaft an.
	pia, err := s.RedeemChallenge(Redemption{Purpose: ChallengeJoin, Email: "pia@example.test", Code: c.Code})
	if err != nil || pia.Name != "pia" || pia.Email != "pia@example.test" {
		t.Fatalf("pia=%+v err=%v", pia, err)
	}
	if role := s.OrgRole(org.ID, pia.ID); role != OrgMember {
		t.Fatalf("role %q", role)
	}
	// Die Einladung ist verbraucht.
	if _, err := s.PreviewJoin(code, false, true); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("invitation still open: %v", err)
	}
}

func TestJoinUnboundMakesTheAccountAtOnceAndConfirmsLater(t *testing.T) {
	s, owner, org := emailFixture(t)
	code, _, _ := s.CreateInvitation(owner.ID, org.ID, "", OrgMember, time.Hour)
	a, c, err := s.BeginJoinByMail(code, "jonas", "Jonas@Example.test")
	if err != nil || a.Name != "jonas" || a.Email != "" || c.Purpose != ChallengeConfirm {
		t.Fatalf("a=%+v c=%+v err=%v", a, c, err)
	}
	if role := s.OrgRole(org.ID, a.ID); role != OrgMember {
		t.Fatalf("not a member: %q", role)
	}
	if s.PendingEmail(a.ID) != "jonas@example.test" {
		t.Fatal("address not pending")
	}
	if _, ok, _ := s.IssueLoginChallenge(ChallengeLogin, "jonas@example.test"); ok {
		t.Fatal("pending address signs in")
	}
}

func TestPasswordResetSetsTheHashAndConsumesTheMail(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "jonas@example.test")
	if s.PasswordHash(jonas.ID) != "" {
		t.Fatal("password before it was set")
	}
	c, ok, _ := s.IssueLoginChallenge(ChallengeReset, "jonas@example.test")
	if !ok {
		t.Fatal("no reset mail")
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeReset, Link: c.Link}); err == nil {
		t.Fatal("reset without a new password")
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeReset, Link: c.Link, PasswordHash: "HASH1"}); err != nil {
		t.Fatal(err)
	}
	if s.PasswordHash(jonas.ID) != "HASH1" {
		t.Fatal("hash not stored")
	}
	a, hash, found := s.PasswordLogin("JONAS@example.test")
	if !found || hash != "HASH1" || a.ID != jonas.ID {
		t.Fatalf("a=%+v hash=%q found=%v", a, hash, found)
	}
	if _, err := s.RedeemChallenge(Redemption{Purpose: ChallengeReset, Link: c.Link, PasswordHash: "HASH2"}); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("reset mail reused: %v", err)
	}
}

func TestMemberLoginLinkOnlyFromOwnerForMember(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "")
	pia := memberWithEmail(t, s, owner, org, "pia", "")
	// Ein Mitglied stellt keinen aus, auch nicht für sich.
	if _, _, err := s.IssueMemberLoginLink(jonas.ID, org.ID, pia.ID); !errors.Is(err, ErrNotOrgOwner) {
		t.Fatalf("member issuing: %v", err)
	}
	// Ein Owner nicht für sich selbst und nicht für Fremde der Organisation.
	if _, _, err := s.IssueMemberLoginLink(owner.ID, org.ID, owner.ID); err == nil {
		t.Fatal("owner issued a link for themselves")
	}
	outsider, _ := s.AddAccount("outsider", "", false)
	if _, _, err := s.IssueMemberLoginLink(owner.ID, org.ID, outsider.ID); !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("outsider: %v", err)
	}
	code, ttl, err := s.IssueMemberLoginLink(owner.ID, org.ID, jonas.ID)
	if err != nil || ttl != LoginLinkTTL {
		t.Fatalf("code=%q ttl=%v err=%v", code, ttl, err)
	}
	a, err := s.RedeemLoginLink(code)
	if err != nil || a.ID != jonas.ID {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	if _, err := s.RedeemLoginLink(code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("link reused: %v", err)
	}
}

func TestWebLoginLinkIsShortBoundToTheTokenAndOneShot(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "")
	_, info, err := s.CreateDeviceToken(jonas.ID, "mainex")
	if err != nil {
		t.Fatal(err)
	}
	code, ttl, err := s.IssueWebLoginLink(jonas.ID, info.ID, "mainex")
	if err != nil || ttl > 120*time.Second {
		t.Fatalf("code=%q ttl=%v err=%v", code, ttl, err)
	}
	var machine string
	var tokenID int64
	s.db.QueryRow(`SELECT machine, token_id FROM account_codes WHERE code_hash=?`, hashToken(code)).Scan(&machine, &tokenID)
	if machine != "mainex" || tokenID != info.ID {
		t.Fatalf("bound to %q/%d", machine, tokenID)
	}
	// Das Token wird widerrufen: der Link taugt nicht mehr.
	if err := s.RevokeToken(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemLoginLink(code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("link of a revoked device works: %v", err)
	}
	_, info2, _ := s.CreateDeviceToken(jonas.ID, "mainex")
	code2, _, _ := s.IssueWebLoginLink(jonas.ID, info2.ID, "mainex")
	if a, err := s.RedeemLoginLink(code2); err != nil || a.ID != jonas.ID {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	if _, err := s.RedeemLoginLink(code2); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("link reused: %v", err)
	}
}

func TestWelcomeCardAfterFirstAccountAndAfterJoining(t *testing.T) {
	s, owner, org := emailFixture(t)
	cards := s.WelcomeCards(owner.ID)
	if len(cards) != 1 || cards[0].OrgID != org.ID || cards[0].OrgName == "" {
		t.Fatalf("owner cards = %+v", cards)
	}
	jonas := memberWithEmail(t, s, owner, org, "jonas", "")
	if c := s.WelcomeCards(jonas.ID); len(c) != 1 || c[0].OrgID != org.ID {
		t.Fatalf("member cards = %+v", c)
	}
	if err := s.DismissWelcome(jonas.ID, org.ID); err != nil {
		t.Fatal(err)
	}
	if c := s.WelcomeCards(jonas.ID); len(c) != 0 {
		t.Fatalf("card after dismissing: %+v", c)
	}
	if c := s.WelcomeCards(owner.ID); len(c) != 1 {
		t.Fatal("dismissing one card removed another account's")
	}
}

func TestMemberLoginLinkIsRefusedForAMemberWhoBelongsElsewhere(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "")
	if !s.LoginLinkAllowed(org.ID, jonas.ID) {
		t.Fatal("a member of this organization only is refused")
	}
	// Jonas also belongs to another organization (of someone else): the owner here must not sign in as him.
	other, _ := s.AddAccount("mara", "", false)
	second, err := s.CreateOrg(other.ID, "Other", "other")
	if err != nil {
		t.Fatal(err)
	}
	inv, _, _ := s.CreateInvitation(other.ID, second.ID, "", OrgMember, time.Hour)
	if _, err := s.AcceptInvitation(jonas.ID, inv); err != nil {
		t.Fatal(err)
	}
	if s.LoginLinkAllowed(org.ID, jonas.ID) {
		t.Fatal("the list offers a login link for a member of two organizations")
	}
	if _, _, err := s.IssueMemberLoginLink(owner.ID, org.ID, jonas.ID); !errors.Is(err, ErrLoginLinkScope) {
		t.Fatalf("err = %v", err)
	}
	var codes int
	s.db.QueryRow(`SELECT COUNT(*) FROM account_codes WHERE kind='login'`).Scan(&codes)
	if codes != 0 {
		t.Fatal("a link was made before the refusal")
	}
	// Nor for a member who has repositories in their own namespace.
	pia := memberWithEmail(t, s, owner, org, "pia", "")
	if _, err := s.ClaimProject(pia.ID, "github.com/pia/own", ""); err == nil {
		if s.LoginLinkAllowed(org.ID, pia.ID) {
			if orgs, _ := s.ListOrgs(pia.ID); len(orgs) > 1 {
				t.Fatal("a member with an own namespace is offered a login link")
			}
		}
	}
}

func TestWebLoginLinkNeedsADeviceToken(t *testing.T) {
	s, owner, org := emailFixture(t)
	jonas := memberWithEmail(t, s, owner, org, "jonas", "")
	_, cli, err := s.CreateToken("jonas", TokenSpec{Label: "pasted"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.IssueWebLoginLink(jonas.ID, cli.ID, "mainex"); !errors.Is(err, ErrDeviceTokenRequired) {
		t.Fatalf("a non-device token: %v", err)
	}
	// A device token of another account does not count either.
	_, other, _ := s.CreateDeviceToken(owner.ID, "mainex")
	if _, _, err := s.IssueWebLoginLink(jonas.ID, other.ID, "mainex"); !errors.Is(err, ErrDeviceTokenRequired) {
		t.Fatalf("a token of another account: %v", err)
	}
}
