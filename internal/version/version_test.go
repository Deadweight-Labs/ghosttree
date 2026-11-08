package version

import "testing"

func TestChannelFollowsTheVersionString(t *testing.T) {
	for v, want := range map[string]string{
		"0.1.0":             ChannelStable,
		"v1.2.3":            ChannelStable,
		"0.1.0-rc.3":        ChannelStable,
		"0.1.0-dev":         ChannelDev,
		"0.1.0-dev+1c48c74": ChannelDev,
		"0.1.0+abc":         ChannelDev,
		"unknown":           ChannelDev,
		"":                  ChannelDev,
	} {
		if got := Channel(v); got != want {
			t.Errorf("Channel(%q) = %s, want %s", v, got, want)
		}
	}
}

func TestCompareOrdersDevAndStable(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     Relation
	}{
		{"0.1.0-dev+1c48c74", "0.1.0-dev+b0cd1b9", Different},
		{"0.1.0-dev+1c48c74", "0.1.0-dev+1c48c74", Same},
		{"0.1.0-dev", "0.1.0", Newer},
		{"0.1.0-dev+abc", "v0.1.0", Newer},
		{"0.1.0", "0.1.0-rc.3", Older},
		{"0.1.0-rc.2", "0.1.0-rc.10", Newer},
		{"0.1.0-rc.3", "0.1.0-dev", Older},
		{"0.1.9", "0.2.0-dev+x", Newer},
		{"1.0.0", "0.9.9", Older},
		{"unknown", "0.1.0", Unknown},
	} {
		if got := Compare(c.from, c.to); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}

func TestCommitComesFromTheVersionString(t *testing.T) {
	if got := Commit("0.1.0-dev+1c48c74"); got != "1c48c74" {
		t.Fatalf("Commit = %q", got)
	}
}

func TestLabel(t *testing.T) {
	if Label("0.2.0") != "v0.2.0" || Label("0.1.0-dev+abc") != "0.1.0-dev+abc" {
		t.Fatal("label wrong")
	}
}
