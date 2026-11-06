package money

import "testing"

func TestString(t *testing.T) {
	cases := map[Cents]string{0: "0.00 EUR", 5: "0.05 EUR", 1234: "12.34 EUR", -250: "-2.50 EUR"}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("%d: got %q want %q", in, got, want)
		}
	}
}

func TestFormatDE(t *testing.T) {
	if got := Cents(123456).FormatDE(); got != "1.234,56 €" {
		t.Fatalf("got %q", got)
	}
	if got := Cents(-5).FormatDE(); got != "-0,05 €" {
		t.Fatalf("got %q", got)
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Cents{"12.34": 1234, "12,3": 1230, "7": 700, "-1,05 €": -105, "3.50 EUR": 350}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.234.5", "1.2345"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestRound(t *testing.T) {
	if Round(5, 2) != 3 || Round(-5, 2) != -3 || Round(4, 3) != 1 {
		t.Fatal("Round")
	}
}

func TestRoundBankers(t *testing.T) {
	cases := []struct {
		num, den int64
		want     Cents
	}{
		{5, 2, 2}, {7, 2, 4}, {-5, 2, -2}, {10, 4, 2}, {11, 4, 3}, {1, 3, 0},
	}
	for _, c := range cases {
		if got := RoundBankers(c.num, c.den); got != c.want {
			t.Errorf("%d/%d: got %d want %d", c.num, c.den, got, c.want)
		}
	}
}
