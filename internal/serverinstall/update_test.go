package serverinstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installed makes a server of version 0.1.0 in a temp root and a release server
// that offers more.
type updateFixture struct {
	prefix string
	sys    *fakeSystem
	env    Env
	rel    Release
	fake   *fakeReleases
	key    testKey
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	prefix := t.TempDir()
	sys := newFakeSystem(t, prefix)
	env, _ := testEnv(sys)
	if _, err := Install(context.Background(), Options{Prefix: prefix, CtxBinary: ctxSource(t, "0.1.0"), Admin: "alex"}, env); err != nil {
		t.Fatal(err)
	}
	sys.cmds = nil
	k := newTestKey(t)
	f := newFakeReleases(t)
	return &updateFixture{prefix: prefix, sys: sys, env: env, rel: f.release(k), fake: f, key: k}
}

func (u *updateFixture) update(t *testing.T, tag string) (*UpdateResult, error) {
	t.Helper()
	return Update(context.Background(), UpdateOptions{Prefix: u.prefix, Version: tag}, u.rel, u.env)
}

func TestUpdateSavesTheDatabaseSwapsTheBinaryAndChecksTheNewVersion(t *testing.T) {
	u := newUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "v:0.2.0\n")
	res, err := u.update(t, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "0.1.0" || res.To != "0.2.0" || res.RolledBack || res.Unchanged {
		t.Errorf("%+v", res)
	}
	if got := u.sys.binVersion(); got != "0.2.0" {
		t.Errorf("the binary is %q", got)
	}
	if v, _ := versionOf(filepath.Join(u.prefix, BinPath+".prev-0.1.0")); v != "0.1.0" {
		t.Errorf("the old binary was not kept: %q", v)
	}
	if left, _ := filepath.Glob(filepath.Join(u.prefix, "usr/local/bin/.ctx-new-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	if bak, _ := os.ReadFile(filepath.Join(u.prefix, res.Backup)); !strings.Contains(string(bak), "db before 0.1.0") {
		t.Errorf("backup %s: %q", res.Backup, bak)
	}
	if !strings.Contains(res.Backup, "pre-update-0.1.0-to-0.2.0-") {
		t.Errorf("backup name %q", res.Backup)
	}
	// Order: stopped, copied by the old binary, then swapped (the schema step
	// and the start are by the new one), started, asked for the new version.
	order := []string{"systemctl stop ghosttree.service", "db backup --db " + DBPath, "upgrade-schema --db " + DBPath, "systemctl start ghosttree.service", "/api/version"}
	last := -1
	for _, c := range order {
		i := u.sys.index(c)
		if i <= last {
			t.Fatalf("%q at %d after %d:\n%s", c, i, last, strings.Join(u.sys.commands(), "\n"))
		}
		last = i
	}
	if !Healthy(res.Checks) {
		t.Errorf("checks after the update: %+v", res.Checks)
	}
}

func TestUpdateTakesTheNewestStableReleaseWhenNoneIsNamed(t *testing.T) {
	u := newUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.3.0", "v:0.3.0\n")
	u.fake.latest = "v0.3.0"
	res, err := u.update(t, "")
	if err != nil || res.To != "0.3.0" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUpdateToTheVersionThatRunsDoesNothing(t *testing.T) {
	u := newUpdateFixture(t)
	res, err := u.update(t, "v0.1.0")
	if err != nil || !res.Unchanged {
		t.Fatalf("%+v %v", res, err)
	}
	if len(u.sys.commands()) > 1 { // only the version of the installed binary
		t.Errorf("it touched the server:\n%s", strings.Join(u.sys.commands(), "\n"))
	}
}

// A release that does not come up must leave the server as it was: the old
// binary, the old database, running.
func TestUpdateGoesBackWhenTheNewReleaseDoesNotAnswer(t *testing.T) {
	u := newUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0-rc.1", "v:0.2.0-rc.1 broken\n") // runs `version`, never answers HTTP
	res, err := u.update(t, "v0.2.0-rc.1")
	if err == nil || !strings.Contains(err.Error(), "runs again") {
		t.Fatalf("error = %v", err)
	}
	if res == nil || !res.RolledBack {
		t.Fatalf("result %+v", res)
	}
	if got := u.sys.binVersion(); got != "0.1.0" {
		t.Errorf("the binary is %q after the rollback", got)
	}
	if !u.sys.saw("db restore --db " + DBPath + " --from " + res.Backup) {
		t.Errorf("the database was not put back:\n%s", strings.Join(u.sys.commands(), "\n"))
	}
	// Restored before the service starts again.
	if i, j := u.sys.index("db restore"), lastIndex(u.sys.commands(), "systemctl start ghosttree.service"); i < 0 || j < i {
		t.Errorf("restore at %d, last start at %d", i, j)
	}
	if !u.sys.running {
		t.Error("the old server is not running")
	}
	if !Healthy(res.Checks) {
		t.Errorf("checks after the rollback: %+v", res.Checks)
	}
}

func lastIndex(cmds []string, sub string) int {
	last := -1
	for i, c := range cmds {
		if strings.Contains(c, sub) {
			last = i
		}
	}
	return last
}

func TestUpdateGoesBackWhenTheSchemaUpgradeFails(t *testing.T) {
	u := newUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "v:0.2.0 badschema\n")
	res, err := u.update(t, "v0.2.0")
	if err == nil || res == nil || !res.RolledBack {
		t.Fatalf("%+v %v", res, err)
	}
	if u.sys.binVersion() != "0.1.0" || !u.sys.running || !u.sys.saw("db restore") {
		t.Errorf("binary %q, running %v:\n%s", u.sys.binVersion(), u.sys.running, strings.Join(u.sys.commands(), "\n"))
	}
}

func TestUpdateRefusesATamperedReleaseBeforeTouchingTheServer(t *testing.T) {
	u := newUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "v:0.2.0\n")
	a := u.fake.get("v0.2.0/ghosttree_0.2.0_linux_amd64.tar.gz")
	a[len(a)/2] ^= 0xff
	u.fake.set("v0.2.0/ghosttree_0.2.0_linux_amd64.tar.gz", a)
	if _, err := u.update(t, "v0.2.0"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
	if u.sys.saw("systemctl stop") || u.sys.binVersion() != "0.1.0" {
		t.Errorf("the server was touched:\n%s", strings.Join(u.sys.commands(), "\n"))
	}
}

func TestUpdateStopsBeforeChangingAnythingWhenTheDatabaseCannotBeCopied(t *testing.T) {
	u := newUpdateFixture(t)
	u.fake.publish(t, u.key, "v0.2.0", "v:0.2.0\n")
	u.sys.failBackup = true
	_, err := u.update(t, "v0.2.0")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("error = %v", err)
	}
	if u.sys.binVersion() != "0.1.0" || !u.sys.running {
		t.Errorf("binary %q, running %v", u.sys.binVersion(), u.sys.running)
	}
}

func TestUpdateNeedsAnInstalledServer(t *testing.T) {
	prefix := t.TempDir()
	env, _ := testEnv(newFakeSystem(t, prefix))
	_, err := Update(context.Background(), UpdateOptions{Prefix: prefix, Version: "v0.2.0"}, Release{}, env)
	if err == nil || !strings.Contains(err.Error(), "ctx server install") {
		t.Fatalf("error = %v", err)
	}
}
