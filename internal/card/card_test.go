package card

import (
	"strings"
	"testing"
)

// The paths are part of every backup's key.
func TestPathIsFixed(t *testing.T) {
	if got := Path("proteus"); got != "m/43'/60'/1581'/1'/178028041'" {
		t.Errorf("proteus's path changed: %s", got)
	}
	if Path(" proteus\n") != Path("proteus") || Path("proteus") == Path("scribe") {
		t.Error("names")
	}
	if !strings.HasPrefix(Path("x"), "m/43'/60'/1581'/") {
		t.Error("outside EIP-1581: a card exports nothing else")
	}
}

// The derivation from the exported key is pinned as well.
func TestDeriveIsFixed(t *testing.T) {
	priv := make([]byte, 32)
	for i := range priv {
		priv[i] = byte(i)
	}
	a, _ := derive(priv, "proteus")
	b, _ := derive(priv, "scribe")
	if len(a) != 64 || a == b {
		t.Fatalf("%s %s", a, b)
	}
	if a != "06b9b2ac54da78a4863035b5dbf82756ee636210245f5445e4252e4dbc818fa4" {
		t.Errorf("the derivation changed: %s", a)
	}
}
