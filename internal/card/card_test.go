package card

import (
	"strings"
	"testing"
)

// The paths are part of every backup's key.
func TestPathIsFixed(t *testing.T) {
	if got := Path("proteus"); got != "m/83696968'/128169'/32'/178028041'" {
		t.Errorf("proteus's path changed: %s", got)
	}
	if Path(" proteus\n") != Path("proteus") || Path("proteus") == Path("scribe") {
		t.Error("names")
	}
	if !strings.HasPrefix(Path("x"), "m/83696968'/128169'/32'/") {
		t.Error("not BIP85 HEX")
	}
}
