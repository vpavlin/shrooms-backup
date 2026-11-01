package backup

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Preset is a named set of what a source keeps: paths (globs, "~" for every
// home on the machine) and what to leave out of them, in restic's patterns:
// one without a leading "/" matches its components at any depth
// ("shrooms-agent/uploads"). Picked by name in the
// config and the apps, so nobody has to know where each thing lives.
//
// From the inventory of 2026-11-01 (ADR-001): what is irreplaceable is
// identities, keys and configuration, a few hundred KB a machine; source
// code is on its forges and caches rebuild themselves, so neither is here.
type Preset struct {
	About   string   `json:"about"`
	Paths   []string `json:"paths"`
	Exclude []string `json:"exclude,omitempty"`
}

// Presets are the ones the daemon knows.
var Presets = map[string]Preset{
	"shrooms": {
		About: "the mesh identity and the daemon's state",
		Paths: []string{"/etc/shrooms", "/var/lib/shrooms", "/etc/systemd/system/shrooms*.service", "/etc/systemd/system/shrooms*.service.d"},
		// Core dumps are no state (316 MB on pi5 on 2026-11-01, the daemon
		// crashing as the machine rebooted).
		Exclude: []string{"/var/lib/shrooms/core", "/var/lib/shrooms/core.*", "*.tmp"},
	},
	"agents": {
		About: "Shrooms Agents: sessions, settings, schedules, the model keys",
		Paths: []string{"~/.local/share/shrooms-agent", "~/.config/shrooms-agent", "/etc/shrooms-agent",
			"~/.pi/agent", "~/.claude/.credentials.json", "~/.claude/settings.json", "~/.claude/CLAUDE.md", "~/.claude.json"},
		// Files sent to sessions are transport, kept two weeks (shrooms
		// ADR-078); conversations are in agent-history.
		// and the event logs (the conversations as the apps show them:
		// 385 MB on jimmy-crib) are in agent-history; pi's bin and npm
		// reinstall themselves, and so does its package cache (git: 202 MB
		// on pi5).
		Exclude: []string{"shrooms-agent/uploads", "shrooms-agent/archive", "shrooms-agent/events",
			".pi/agent/sessions", ".pi/agent/bin", ".pi/agent/npm", ".pi/agent/git"},
	},
	"agent-history": {
		About: "the agents' conversations (larger: hundreds of MB a machine)",
		Paths: []string{"~/.local/share/shrooms-agent/events", "~/.pi/agent/sessions", "~/.claude/projects"},
	},
	"foray": {
		About: "Foray's lists, its config and each list's assistant settings",
		// An assistant's folder is AGENTS.md and .pi; its browser (2.8 GB) and
		// tools reinstall themselves. Not "~/foray-*": on jimmy-crib that is
		// also foray-dist, the APK builds.
		Paths: []string{"~/.config/foray", "~/.local/share/foray", "~/foray-*/AGENTS.md", "~/foray-*/.pi"},
	},
	"keys": {
		About: "SSH, GPG and API tokens; signing keystores; repository and CA keys",
		Paths: []string{"~/.ssh", "/root/.ssh", "~/.gnupg", "~/.config/gh", "~/.docker/config.json",
			"~/apk-signing", "~/keystores", "~/mesh-ca", "~/.backpack",
			"~/*fdroid*/keystore.p12", "~/*fdroid*/config.yml"},
		Exclude: []string{".gnupg/S.*", ".gnupg/*.lock"},
	},
	"services": {
		About: "hand-written units and service configs",
		Paths: []string{"/etc/caddy", "/etc/systemd/system/*.service", "/etc/systemd/system/*.timer",
			"~/.config/systemd/user", "/root/*/.env", "/etc/shrooms-backup/config.json"},
	},
}

// homes are the users' home directories on this machine.
func homes() []string {
	out := []string{}
	m, _ := filepath.Glob("/home/*")
	for _, h := range m {
		if fi, err := os.Stat(h); err == nil && fi.IsDir() {
			out = append(out, h)
		}
	}
	return out
}

// expand turns a preset's glob into the paths that exist here.
func expand(glob string, homes []string) []string {
	var pats []string
	if len(glob) > 1 && glob[:2] == "~/" {
		for _, h := range homes {
			pats = append(pats, filepath.Join(h, glob[2:]))
		}
	} else {
		pats = []string{glob}
	}
	var out []string
	for _, p := range pats {
		m, _ := filepath.Glob(p)
		out = append(out, m...)
	}
	return out
}

// Resolve is a source's paths and excludes, its presets expanded on this
// machine: only paths that exist, since restic refuses a missing one. The
// backup's own key is never among them: a backup cannot hold the key to
// itself.
func (s *SourceConfig) Resolve() (paths, exclude []string) {
	hs := homes()
	add := func(p string) {
		if p != keyPath && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	for _, name := range s.Presets {
		pr := Presets[name]
		for _, g := range pr.Paths {
			for _, p := range expand(g, hs) {
				add(p)
			}
		}
		exclude = append(exclude, pr.Exclude...)
	}
	for _, p := range s.Paths {
		if _, err := os.Lstat(p); err == nil {
			add(p)
		}
	}
	exclude = append(exclude, s.Exclude...)
	exclude = append(exclude, keyPath)
	sort.Strings(paths)
	return paths, slices.Compact(exclude)
}

// keyPath is where a source's key lives; never backed up.
var keyPath = "/etc/shrooms-backup/key"

// Excluded reports whether path is left out by a restic exclude pattern: an
// absolute pattern matches from the root, a relative one any run of the
// path's trailing components (restic's rule, without "**").
func Excluded(pattern, path string) bool {
	pc := strings.Split(strings.Trim(path, "/"), "/")
	if strings.HasPrefix(pattern, "/") {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	qc := strings.Split(pattern, "/")
	for start := 0; start+len(qc) <= len(pc); start++ {
		all := true
		for i, q := range qc {
			if ok, _ := filepath.Match(q, pc[start+i]); !ok {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
