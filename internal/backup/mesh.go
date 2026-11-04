package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// Mesh is what the shrooms daemon says about the meshes this machine is in:
// its own name and addresses, and who every other address is.
type Mesh struct {
	Socket string // the daemon's control socket

	mu     sync.Mutex
	st     meshStatus
	names  map[netip.Addr]string
	meshOf map[netip.Addr]string // the mesh an address is on
}

type meshStatus struct {
	Name   string `json:"name"`
	Meshes []struct {
		Label    string `json:"label"`
		Overlay  string `json:"overlay"`
		Disabled bool   `json:"disabled"`
	} `json:"meshes"`
	Peers []struct {
		Name      string `json:"name"`
		Mesh      string `json:"mesh"`
		Overlay   string `json:"overlay"`
		OverlayV4 string `json:"overlay_v4"`
		Online    *bool  `json:"online"`
	} `json:"peers"`
}

// Refresh reads the daemon's status.
func (m *Mesh) Refresh(ctx context.Context) error {
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", m.Socket)
		}}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://shrooms/status", nil)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("the shrooms daemon (%s): %w", m.Socket, err)
	}
	defer resp.Body.Close()
	var st meshStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return err
	}
	names, meshOf := map[netip.Addr]string{}, map[netip.Addr]string{}
	for _, p := range st.Peers {
		for _, s := range []string{p.Overlay, p.OverlayV4} {
			if a, err := netip.ParseAddr(s); err == nil {
				names[a.Unmap()], meshOf[a.Unmap()] = p.Name, p.Mesh
			}
		}
	}
	// This machine, calling from its own mesh address, is itself.
	for _, me := range st.Meshes {
		if a, err := netip.ParseAddr(me.Overlay); err == nil {
			names[a.Unmap()], meshOf[a.Unmap()] = st.Name, me.Label
		}
	}
	m.mu.Lock()
	m.st, m.names, m.meshOf = st, names, meshOf
	m.mu.Unlock()
	return nil
}

// Self is this machine's name.
func (m *Mesh) Self() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Name
}

// Who is the device at a mesh address, "" for one the mesh does not know.
func (m *Mesh) Who(a netip.Addr) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.names[a.Unmap()]
}

// WhoOn is the device at a mesh address and the mesh the address is on,
// "" for what the mesh does not know (the mesh is "" where Set gave none).
func (m *Mesh) WhoOn(a netip.Addr) (name, mesh string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.names[a.Unmap()], m.meshOf[a.Unmap()]
}

// Addresses are this machine's overlay addresses, by mesh label.
func (m *Mesh) Addresses() map[string]netip.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]netip.Addr{}
	for _, me := range m.st.Meshes {
		if a, err := netip.ParseAddr(me.Overlay); err == nil && !me.Disabled {
			out[me.Label] = a
		}
	}
	return out
}

// Find is a device's address on mesh, or, with mesh "", on any mesh this
// machine shares with it. A device that is only on another mesh is not
// found on this one: a source's copies are fetched from its node on the mesh
// it backs up over, which a mirror elsewhere cannot reach.
func (m *Mesh) Find(device, mesh string) (netip.Addr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var any netip.Addr
	for _, p := range m.st.Peers {
		if p.Name != device {
			continue
		}
		a, err := netip.ParseAddr(p.Overlay)
		if err != nil {
			continue
		}
		if p.Mesh == mesh {
			return a, true
		}
		if mesh == "" && !any.IsValid() {
			any = a
		}
	}
	return any, any.IsValid()
}

// Devices are the names of the devices on the meshes, without this machine.
func (m *Mesh) Devices() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, p := range m.st.Peers {
		if !seen[p.Name] && p.Name != m.st.Name {
			seen[p.Name] = true
			out = append(out, p.Name)
		}
	}
	return out
}

// Set gives the mesh's state directly (tests, and a machine with no daemon).
func (m *Mesh) Set(self string, peers map[string]netip.Addr) {
	st := meshStatus{Name: self}
	names := map[netip.Addr]string{}
	for name, a := range peers {
		st.Peers = append(st.Peers, struct {
			Name      string `json:"name"`
			Mesh      string `json:"mesh"`
			Overlay   string `json:"overlay"`
			OverlayV4 string `json:"overlay_v4"`
			Online    *bool  `json:"online"`
		}{Name: name, Overlay: a.String()})
		names[a.Unmap()] = name
	}
	m.mu.Lock()
	m.st, m.names = st, names
	m.mu.Unlock()
}

// SetPeers gives the mesh's state with each peer's mesh (tests).
func (m *Mesh) SetPeers(self string, peers []Peer) {
	st := meshStatus{Name: self}
	names, meshOf := map[netip.Addr]string{}, map[netip.Addr]string{}
	for _, p := range peers {
		st.Peers = append(st.Peers, struct {
			Name      string `json:"name"`
			Mesh      string `json:"mesh"`
			Overlay   string `json:"overlay"`
			OverlayV4 string `json:"overlay_v4"`
			Online    *bool  `json:"online"`
		}{Name: p.Name, Mesh: p.Mesh, Overlay: p.Overlay})
		a := netip.MustParseAddr(p.Overlay).Unmap()
		names[a], meshOf[a] = p.Name, p.Mesh
	}
	m.mu.Lock()
	m.st, m.names, m.meshOf = st, names, meshOf
	m.mu.Unlock()
}

// Peer is a device on this machine's meshes, as the apps see it.
type Peer struct {
	Name      string `json:"name"`
	Mesh      string `json:"mesh"`
	Overlay   string `json:"overlay"`
	OverlayV4 string `json:"overlay_v4,omitempty"`
	Online    bool   `json:"online"`
}

// Peers are the devices on this machine's meshes, one entry a mesh.
func (m *Mesh) Peers() []Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Peer{}
	for _, p := range m.st.Peers {
		out = append(out, Peer{Name: p.Name, Mesh: p.Mesh, Overlay: p.Overlay, OverlayV4: p.OverlayV4, Online: p.Online == nil || *p.Online})
	}
	return out
}
