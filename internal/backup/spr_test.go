package backup

import (
	"strings"
	"testing"
)

// Two SPRs of k11's node, before and after a restart (only the sequence
// number differs), and scribe's home node: real ones, 2026-11-05.
const (
	k11Before = "spr:CiUIAhIhAocKyDg0D32tpdQJLT6EfD53O7lbeQHbt_hAJhqUKSiPEgIDARpHCicAJQgCEiEChwrIODQPfa2l1AktPoR8Pnc7uVt5Adu3-EAmGpQpKI8Qi6aw1wYaFgoUKf17FftewfyXIKObXJ_Ec0gGIY4qRjBEAiBlcK9pjj51lglX0dJNx0FD_56IcaYkz_YFJ-H7JGERHwIgBg-nzRJwtiUB5zguitlcEEXZlVw44v2CPKQ5Y0U_l5g"
	k11After  = "spr:CiUIAhIhAocKyDg0D32tpdQJLT6EfD53O7lbeQHbt_hAJhqUKSiPEgIDARpHCicAJQgCEiEChwrIODQPfa2l1AktPoR8Pnc7uVt5Adu3-EAmGpQpKI8QvtOw1wYaFgoUKf17FftewfyXIKObXJ_Ec0gGIY4qRzBFAiEAsoG4VoloTYuiDAqMnBbB3hRASxp-XuczrytCaRIv-y8CIGaO6sx8HoaNvCPQ7bPtVE_0WtmSH96iIgCQ35HgBhcI"
	scribe    = "spr:CiUIAhIhA4WJhFCf6UgnsmJ43BAfGUtIacnW6I5Y0Uc2smDuN-j9EgIDARpHCicAJQgCEiEDhYmEUJ_pSCeyYnjcEB8ZS0hpydbojljRRzayYO436P0QwNOw1wYaFgoUKf17FftewSaCJT7WiKb9uSQGIZAqRzBFAiEAxTaY6IKOFtIW_0xo10HXAAbw87XSEqebcT3soMvfC3cCIGlvE9EkcqnhV50JrXhBYT179KpWIqhQMPPRS-iQ-dEV"
)

func TestSPRIdentity(t *testing.T) {
	if k11Before == k11After {
		t.Fatal("the fixtures are the same SPR")
	}
	if sprIdentity(k11Before) != sprIdentity(k11After) {
		t.Error("a restart of the same node, same addresses, reads as a different node")
	}
	if sprIdentity(k11After) == sprIdentity(scribe) {
		t.Error("two nodes read as one")
	}
	if !strings.Contains(sprIdentity(k11After), "\x00") {
		t.Errorf("k11's SPR did not parse: %q", sprIdentity(k11After))
	}
	if sprIdentity("spr:not base64!") != "spr:not base64!" || sprIdentity("") != "" {
		t.Error("an SPR that does not parse is its own identity")
	}
}
