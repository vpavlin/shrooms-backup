package backup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Presets expand to what exists, in every home; the key never goes in;
// a missing path is dropped rather than failing the run.
func TestResolve(t *testing.T) {
	root := t.TempDir()
	h := filepath.Join(root, "home", "me")
	for _, p := range []string{".ssh/id_ed25519", "foray-home/AGENTS.md", "foray-home/.browser/x", ".config/foray/config.json"} {
		os.MkdirAll(filepath.Dir(filepath.Join(h, p)), 0o700)
		os.WriteFile(filepath.Join(h, p), []byte("x"), 0o600)
	}
	saved := Presets
	defer func() { Presets = saved }()
	Presets = map[string]Preset{
		"keys":  {Paths: []string{"~/.ssh", "~/.gnupg"}},
		"foray": {Paths: []string{"~/.config/foray", "~/foray-*"}, Exclude: []string{"foray-*/.browser"}},
	}
	got := expand("~/foray-*", []string{h})
	if len(got) != 1 || got[0] != filepath.Join(h, "foray-home") {
		t.Errorf("expand: %v", got)
	}
	s := &SourceConfig{Presets: []string{"keys", "foray"}, Paths: []string{filepath.Join(root, "missing"), keyPath}}
	// homes() reads /home; Resolve is checked through expand above, and
	// here for the key and the excludes.
	_, ex := s.Resolve()
	if !slices.Contains(ex, keyPath) || !slices.Contains(ex, "foray-*/.browser") {
		t.Errorf("excludes: %v", ex)
	}
	paths, _ := s.Resolve()
	if slices.Contains(paths, keyPath) || slices.Contains(paths, filepath.Join(root, "missing")) {
		t.Errorf("paths: %v", paths)
	}
}

func TestPresetsAreKnownByConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte(`{"mesh":"office","source":{"presets":["keys","nope"],"mirrors":["proteus"]}}`), 0o600)
	if _, err := LoadConfig(p); err == nil {
		t.Error("an unknown preset was accepted")
	}
	os.WriteFile(p, []byte(`{"mesh":"office","source":{"presets":["shrooms","keys"],"mirrors":["proteus"]}}`), 0o600)
	if _, err := LoadConfig(p); err != nil {
		t.Error(err)
	}
}

func TestExcludedLikeRestic(t *testing.T) {
	for _, c := range []struct {
		pat, path string
		want      bool
	}{
		{"shrooms-agent/uploads", "/home/me/.local/share/shrooms-agent/uploads", true},
		{"shrooms-agent/uploads", "/home/me/.local/share/shrooms-agent/sessions.json", false},
		{"foray-*/.browser", "/home/me/foray-home/.browser", true},
		{"*.tmp", "/var/lib/shrooms/seqmarks.json.tmp", true},
		{"/var/lib/shrooms/core", "/var/lib/shrooms/core", true},
		{"/var/lib/shrooms/core", "/x/var/lib/shrooms/core", false},
	} {
		if got := Excluded(c.pat, c.path); got != c.want {
			t.Errorf("Excluded(%q, %q) = %v", c.pat, c.path, got)
		}
	}
}
