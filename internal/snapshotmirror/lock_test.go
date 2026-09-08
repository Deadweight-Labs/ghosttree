package snapshotmirror

import "testing"

func TestLockIDCaseFoldsAndNormalizesWindowsSpellings(t *testing.T) {
	one := lockIDForIdentity(`C:\Work\Ghosttree`, true)
	two := lockIDForIdentity(`c:/work/ghosttree`, true)
	if one != two {
		t.Fatalf("Windows-equivalent identities differ: %q != %q", one, two)
	}
	if lockIDForIdentity(`/Case/Sensitive`, false) == lockIDForIdentity(`/case/sensitive`, false) {
		t.Fatal("case-sensitive identities collapsed")
	}
}
