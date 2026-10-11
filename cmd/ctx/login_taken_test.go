package main

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A machine name that belongs to another account used to end in a dead end: the
// browser said so, the terminal waited until the code expired. Now the terminal
// stops at once and names the exact command to run again (acceptance finding 8).
func TestLoginWithATakenMachineNameStopsAndNamesTheRetry(t *testing.T) {
	for _, via := range []string{"browser refuses", "token exchange refuses"} {
		t.Run(via, func(t *testing.T) {
			url, st, out, clock := loginFixture(t, true)
			if _, err := st.AddPerson("bob"); err != nil {
				t.Fatal(err)
			}
			if err := st.ClaimMachine("build-box", "person:2"); err != nil {
				t.Fatal(err)
			}
			calls := 0
			loginSleep = func(ctx context.Context, d time.Duration) error {
				calls++
				*clock = clock.Add(d + time.Second)
				if calls == 2 {
					m := regexp.MustCompile(`[A-Z0-9]{4}-[A-Z0-9]{4}`).FindString(out.String())
					if via == "browser refuses" {
						if err := st.Device().RefuseMachineTaken(m, "person:1"); err != nil {
							t.Fatal(err)
						}
					} else if err := st.Device().Decide(m, "person:1", true); err != nil {
						t.Fatal(err)
					}
				}
				if calls > 4 {
					t.Fatalf("the terminal keeps waiting:\n%s", out)
				}
				return nil
			}
			if code := cmdLogin([]string{"--server", url, "--machine", "build-box"}, out); code != 1 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			for _, want := range []string{"build-box", "belongs to another account", "login --server " + url + " --machine build-box-2"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out.String(), "server answered") {
				t.Errorf("a raw server code reaches the user:\n%s", out)
			}
		})
	}
}
