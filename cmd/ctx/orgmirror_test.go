package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Deadweight-Labs/ghosttree/internal/forge"
	"github.com/Deadweight-Labs/ghosttree/internal/store"
)

type fakeProfiler map[string]forge.OrgProfile

func (f fakeProfiler) OrgProfile(_ context.Context, _ int64, login string) (forge.OrgProfile, error) {
	p, ok := f[login]
	if !ok {
		return forge.OrgProfile{}, errors.New("unknown")
	}
	return p, nil
}

func TestDailyOrgMirrorTakesOverNameAndAddress(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddAccount("robin", "", true); err != nil {
		t.Fatal(err)
	}
	o, err := st.CreateOrg("person:1", "Default", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConnectSourceWithInstallation("person:1", o.ID, store.SourceGitHub, "deadweight-labs", 179, nil); err != nil {
		t.Fatal(err)
	}
	profiler := fakeProfiler{"deadweight-labs": {Login: "Deadweight-Labs", Name: "Deadweight Labs"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Before the owner opts in only the picture follows (security review 4).
	mirrorOrgsOnce(context.Background(), st, profiler, logger)
	if got, _ := st.OrgByRef("default"); got.Slug != "default" || got.AvatarLogin != "Deadweight-Labs" {
		t.Fatalf("org without opt-in = %+v", got)
	}
	if err := st.SetOrgFollowGitHub("person:1", o.ID, true); err != nil {
		t.Fatal(err)
	}
	mirrorOrgsOnce(context.Background(), st, fakeProfiler{"deadweight-labs": {Login: "Deadweight-Labs", Name: "Deadweight Labs"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got, err := st.OrgByRef("default")
	if err != nil || got.Slug != "deadweight-labs" || got.Name != "Deadweight Labs" {
		t.Fatalf("org after the mirror = %+v %v", got, err)
	}
	// Without a GitHub App nothing runs and the wait returns at once.
	startOrgMirror(context.Background(), st, nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))()
}
