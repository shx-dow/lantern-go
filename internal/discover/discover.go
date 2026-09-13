// Package discover proves nearby Lantern peers can find each other without
// share codes: periodic UDP multicast announces plus a listener feeding a
// last-seen registry.
//
// Wire format is JSON with an "app":"lantern" discriminator, sent to the
// LocalSend multicast group (224.0.0.167) but on Lantern's own port, so real
// LocalSend apps never see us until protocol interop becomes a conscious
// decision. The mechanics (announce → unicast reply → subnet-scan fallback)
// deliberately mirror the LocalSend route: only the payload is ours.
//
// This is a spike: no daemon wiring yet. The intended consumer is lanternd,
// which will merge Registry output with libp2p/mDNS peers for the nearby
// Send screen, and eventually accept tap-to-send offers.
package discover

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// Group is the multicast group for announces. Shared with LocalSend's
	// group because some mobile stacks only allow 224.0.0.0/24; the Port
	// keeps our traffic separate from theirs.
	Group = "224.0.0.167"
	// DefaultPort is Lantern's discovery port. LocalSend uses 53317.
	DefaultPort = 43781
	// ProtoVersion is the spike wire version, bumped on breaking changes.
	ProtoVersion = "0.1"
	// DefaultInterval is how often to re-announce presence.
	DefaultInterval = 2 * time.Second
	// DefaultTTL is how long a silent peer stays listed.
	DefaultTTL = 10 * time.Second
)

// DeviceType mirrors the LocalSend enum so a future interop layer maps
// 1:1. Unknown values fall back to TypeDesktop.
type DeviceType string

const (
	TypeMobile   DeviceType = "mobile"
	TypeDesktop  DeviceType = "desktop"
	TypeHeadless DeviceType = "headless"
	TypeServer   DeviceType = "server"
)

// Device is a nearby Lantern peer as seen on the wire.
type Device struct {
	Alias       string
	Version     string
	Model       string
	Type        DeviceType
	Fingerprint string
	Port        int
	Addr        string // sender address, transport detail, not serialized
}

// message is the wire encoding. App discriminates us from LocalSend
// traffic on the shared group; Announce=false marks a directed reply.
type message struct {
	App         string     `json:"app"`
	Alias       string     `json:"alias"`
	Version     string     `json:"version"`
	Model       string     `json:"model,omitempty"`
	Type        DeviceType `json:"type,omitempty"`
	Fingerprint string     `json:"fingerprint"`
	Port        int        `json:"port"`
	Announce    bool       `json:"announce"`
}

// NewFingerprint mints a random peer identity for this run. The daemon will
// eventually persist one per data-dir; the spike keeps it ephemeral.
func NewFingerprint() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// GroupAddr resolves the multicast destination for port.
func GroupAddr(port int) (*net.UDPAddr, error) {
	return net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", Group, port))
}

// ListenPacketConn joins the multicast group on port and returns a socket
// for ListenLoop. A nil interface uses the system default route, which is
// exactly the case that must work where explicit multicast-interface
// configuration fails (seen on Windows).
func ListenPacketConn(port int, ifi *net.Interface) (*net.UDPConn, error) {
	addr, err := GroupAddr(port)
	if err != nil {
		return nil, err
	}
	return net.ListenMulticastUDP("udp", ifi, addr)
}

// AnnounceOnce serializes dev and sends one packet to dst. Dst is usually
// the multicast group, but tests pass 127.0.0.1 loopback instead.
func AnnounceOnce(dst *net.UDPAddr, dev Device) error {
	raw, err := json.Marshal(message{
		App:         "lantern",
		Alias:       dev.Alias,
		Version:     ProtoVersion,
		Model:       dev.Model,
		Type:        dev.Type,
		Fingerprint: dev.Fingerprint,
		Port:        dev.Port,
		Announce:    true,
	})
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(raw)
	return err
}

// AnnounceLoop re-announces dev until ctx ends.
func AnnounceLoop(ctx context.Context, dst *net.UDPAddr, dev Device, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	_ = AnnounceOnce(dst, dev)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = AnnounceOnce(dst, dev)
		}
	}
}

// ListenLoop reads packets from conn until ctx ends, invoking onDevice for
// every well-formed Lantern announce that is not our own fingerprint.
// Non-Lantern traffic (including real LocalSend announces sharing the
// group) is ignored.
func ListenLoop(ctx context.Context, conn *net.UDPConn, selfFingerprint string, onDevice func(Device)) {
	var buf [2048]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := conn.ReadFromUDP(buf[:])
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue // timeout or transient; keep listening
		}
		var m message
		if err := json.Unmarshal(buf[:n], &m); err != nil {
			continue
		}
		if m.App != "lantern" || m.Fingerprint == "" || m.Fingerprint == selfFingerprint {
			continue
		}
		typ := m.Type
		switch typ {
		case TypeMobile, TypeDesktop, TypeHeadless, TypeServer:
		default:
			typ = TypeDesktop
		}
		onDevice(Device{
			Alias:       m.Alias,
			Version:     m.Version,
			Model:       m.Model,
			Type:        typ,
			Fingerprint: m.Fingerprint,
			Port:        m.Port,
			Addr:        addr.String(),
		})
	}
}

// Registry is a thread-safe last-seen index of nearby devices.
type Registry struct {
	mu   sync.Mutex
	seen map[string]entry
}

type entry struct {
	dev Device
	at  time.Time
}

// NewRegistry builds an empty nearby index.
func NewRegistry() *Registry {
	return &Registry{seen: map[string]entry{}}
}

// Seen records a sighting, returning true when the fingerprint is new.
func (r *Registry) Seen(dev Device) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.seen[dev.Fingerprint]
	r.seen[dev.Fingerprint] = entry{dev: dev, at: time.Now()}
	return !exists
}

// Active returns devices seen within ttl, oldest first.
func (r *Registry) Active(ttl time.Duration) []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-ttl)
	var out []Device
	for _, e := range r.seen {
		if e.at.After(cutoff) {
			out = append(out, e.dev)
		}
	}
	return out
}

// Prune drops devices silent for longer than ttl and returns the ones
// removed, so callers can print departures.
func (r *Registry) Prune(ttl time.Duration) []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-ttl)
	var gone []Device
	for fp, e := range r.seen {
		if !e.at.After(cutoff) {
			delete(r.seen, fp)
			gone = append(gone, e.dev)
		}
	}
	return gone
}
