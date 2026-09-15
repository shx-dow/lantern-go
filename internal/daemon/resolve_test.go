package daemon

import (
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func TestResolveDefaults(t *testing.T) {
	r := Resolve(ConfigFile{}, Flags{P2PPort: -1, DefaultTTL: -1, LAN: true})
	if r.Addr != DefaultAddr {
		t.Fatalf("addr = %q", r.Addr)
	}
	if len(r.BootstrapPeers) != 1 || r.BootstrapPeers[0] != "none" {
		t.Fatalf("bootstrap = %v", r.BootstrapPeers)
	}
	if !r.LANOnly || r.DefaultTTL != 0 || r.DeviceName == "" {
		t.Fatalf("bad defaults: %+v", r)
	}
}

func TestResolveFlagBeatsFile(t *testing.T) {
	file := ConfigFile{
		Addr: "127.0.0.1:1111", P2PPort: 4001, DataDir: "/file",
		DeviceName: "filebox", SharedDirs: []string{"/srv"},
		BootstrapPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/QmX"},
		RelayAddrs:     []string{"/ip4/5.6.7.8/tcp/4001/p2p/QmY"},
		LANOnly:        boolPtr(false), DefaultTTLSeconds: 600,
	}
	f := Flags{
		Addr: "127.0.0.1:2222", P2PPort: 5001, DataDir: "/flag",
		DeviceName: "flagbox", SharedDirs: "/a, /b",
		Bootstrap: "/ip4/9.9.9.9/tcp/4001/p2p/QmZ", Relay: "/ip4/8.8.8.8/tcp/4001/p2p/QmW",
		DefaultTTL: 60, LAN: true, LANSet: true,
	}
	r := Resolve(file, f)
	if r.Addr != "127.0.0.1:2222" || r.P2PPort != 5001 || r.DataDir != "/flag" {
		t.Fatalf("flag did not win: %+v", r)
	}
	if r.DeviceName != "flagbox" || len(r.SharedDirs) != 2 {
		t.Fatalf("flag did not win: %+v", r)
	}
	if len(r.BootstrapPeers) != 1 || len(r.RelayAddrs) != 1 {
		t.Fatalf("flag did not win: %+v", r)
	}
	if r.DefaultTTL != 60*time.Second || !r.LANOnly {
		t.Fatalf("flag did not win: %+v", r)
	}
}

func TestResolveFileBeatsDefault(t *testing.T) {
	file := ConfigFile{
		DeviceName: "filebox", SharedDirs: []string{"/srv"},
		LANOnly: boolPtr(true), DefaultTTLSeconds: 600,
	}
	r := Resolve(file, Flags{P2PPort: -1, DefaultTTL: -1, LAN: true})
	if r.DeviceName != "filebox" || len(r.SharedDirs) != 1 {
		t.Fatalf("file did not win: %+v", r)
	}
	if r.DefaultTTL != 600*time.Second || !r.LANOnly {
		t.Fatalf("file did not win: %+v", r)
	}
}

func TestResolveExplicitBootstrapBeatsLANOnly(t *testing.T) {
	r := Resolve(ConfigFile{}, Flags{P2PPort: -1, DefaultTTL: -1, LAN: true, Bootstrap: "/ip4/9.9.9.9/tcp/4001/p2p/QmZ"})
	if len(r.BootstrapPeers) != 1 || r.BootstrapPeers[0] == "none" {
		t.Fatalf("explicit bootstrap lost: %+v", r.BootstrapPeers)
	}
}

func TestSplitCSV(t *testing.T) {
	got := SplitCSV(" /a ,,/b , ")
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("bad csv: %v", got)
	}
}
