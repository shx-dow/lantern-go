package doctor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// stubDaemon serves the subset of the v1 API doctor reads, so the checks can
// be exercised without a libp2p node.
type stubDaemon struct {
	status    lanternclient.Status
	statusErr int // non-zero returns this HTTP status
	devices   []lanternclient.Device
	devicesNo int
	probe     string
	authed    string
}

func (s *stubDaemon) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		if s.statusErr != 0 {
			w.WriteHeader(s.statusErr)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		_ = json.NewEncoder(w).Encode(s.status)
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		s.probe = r.URL.Query().Get("probe")
		devices := s.devices
		if s.devicesNo != 0 {
			w.WriteHeader(s.devicesNo)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "pairing is not configured"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": devices})
	})
	return httptest.NewServer(mux)
}

func find(t *testing.T, r Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in report: %+v", name, r.Checks)
	return Check{}
}

func boolp(b bool) *bool { return &b }

// A running daemon with routable addresses and a known data dir must report
// healthy: doctor should not cry wolf on a setup that works.
func TestRunHealthySetup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{
		status: lanternclient.Status{
			PeerID: "p1", DeviceName: "wsl",
			Addrs: []string{
				"/ip4/127.0.0.1/tcp/41001",
				"/ip4/172.27.236.57/tcp/41001",
				"/ip4/127.0.0.1/udp/41001/quic-v1",
			},
		},
		devices: []lanternclient.Device{{PeerID: "p2", Alias: "windows", Online: true, KnownAddresses: 15}},
	}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	if !r.Healthy {
		t.Fatalf("healthy setup reported unhealthy: %+v", r.Checks)
	}
	if c := find(t, r, "daemon"); c.Status != Pass {
		t.Fatalf("daemon check = %+v", c)
	}
	if c := find(t, r, "listen-addresses"); c.Status != Pass {
		t.Fatalf("listen-addresses = %+v", c)
	}
	if c := find(t, r, "paired-devices"); c.Status != Pass {
		t.Fatalf("paired-devices = %+v", c)
	}
	if r.Failed() {
		t.Fatalf("Failed() disagrees with Healthy: %+v", r.Checks)
	}
}

// No daemon at all is the most common first-run state, and it must report one
// clear failure rather than five identical ones.
func TestRunWithoutDaemonStopsAfterOneFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	r := Run(t.Context(), lanternclient.New("http://127.0.0.1:1", "tok"), Options{Token: "tok"})
	if r.Healthy || !r.Failed() {
		t.Fatalf("expected failure: %+v", r.Checks)
	}
	if c := find(t, r, "daemon"); c.Status != Fail || c.Fix == "" {
		t.Fatalf("daemon check must fail with a fix: %+v", c)
	}
	// The token check is a local finding, so it still runs; the rest are all
	// daemon-dependent and must be absent rather than reported as failing.
	for _, c := range r.Checks {
		if c.Name == "paired-devices" || c.Name == "listen-addresses" {
			t.Fatalf("%s should not be reported without a daemon: %+v", c.Name, c)
		}
	}
}

// A 401 is a different problem from no daemon, and the fix differs: a token,
// not a restart.
func TestRunReportsAuthFailureDistinctly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{statusErr: http.StatusUnauthorized}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "wrong"), Options{Token: "wrong"})
	c := find(t, r, "daemon")
	if c.Status != Fail || c.Detail == "" {
		t.Fatalf("daemon check = %+v", c)
	}
	if !contains(c.Detail, "401") && !contains(c.Fix, "TOKEN") {
		t.Fatalf("auth failure should name the token: %+v", c)
	}
}

// A missing token is reported locally, before any call, because every call
// would otherwise be refused for the same reason.
func TestRunReportsMissingToken(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	r := Run(t.Context(), lanternclient.New("http://127.0.0.1:1", ""), Options{})
	c := find(t, r, "token")
	if c.Status != Fail || c.Fix == "" {
		t.Fatalf("token check = %+v", c)
	}
}

// Only loopback or link-local addresses means no other device can dial this
// one — the exact silent failure behind "it says online but nothing works".
func TestRunFlagsLoopbackOnlyAddresses(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{status: lanternclient.Status{
		PeerID: "p1", DeviceName: "box",
		Addrs: []string{"/ip4/127.0.0.1/tcp/41001", "/ip4/169.254.1.1/tcp/41001"},
	}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	c := find(t, r, "listen-addresses")
	if c.Status != Fail || !contains(c.Detail, "loopback") {
		t.Fatalf("listen-addresses = %+v", c)
	}
}

// A random libp2p port silently invalidates every --peer address on restart,
// which reads like the other device going offline. It must be surfaced. The
// signal is the daemon's reported p2p_port, not the observed address, because
// any observed port looks plausible and guessing would miss or misfire.
func TestRunWarnsAboutRandomP2PPort(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{status: lanternclient.Status{
		PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/42587"},
	}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	c := find(t, r, "p2p-port")
	if c.Status != Warn || !contains(c.Fix, "--p2p-port") {
		t.Fatalf("p2p-port = %+v", c)
	}
	if !contains(c.Detail, "random") {
		t.Fatalf("the warning should say the port is random: %+v", c)
	}

	// A pinned port must not warn, whatever the port number: an operator's
	// deliberate choice is not a defect.
	s.status.P2PPort = 41001
	s.status.Addrs = []string{"/ip4/192.168.1.11/tcp/41001"}
	r = Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	if c := find(t, r, "p2p-port"); c.Status != Pass {
		t.Fatalf("pinned port should pass: %+v", c)
	}
	s.status.P2PPort = 55555
	r = Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	if c := find(t, r, "p2p-port"); c.Status != Pass {
		t.Fatalf("an unusual but pinned port should pass: %+v", c)
	}
}

// Probing is opt-in and must reach the daemon as ?probe=1; without the flag
// no dial happens at all.
func TestRunProbesPeersOnlyWhenAsked(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{
		status: lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/41001"}},
		devices: []lanternclient.Device{{
			PeerID: "p2", Alias: "windows", Reachable: boolp(false),
			Error: "all dials failed", KnownAddresses: 1,
		}},
	}
	srv := s.start(t)
	defer srv.Close()
	c := lanternclient.New(srv.URL, "tok")

	Run(t.Context(), c, Options{Token: "tok"})
	if s.probe != "" {
		t.Fatalf("probe requested without the option: %q", s.probe)
	}

	r := Run(t.Context(), c, Options{Token: "tok", ProbePeers: true})
	if s.probe != "1" {
		t.Fatalf("probe flag not forwarded: %q", s.probe)
	}
	if d := find(t, r, "paired-devices"); d.Status != Warn || !contains(d.Detail, "unreachable") {
		t.Fatalf("unreachable peer must be reported: %+v", d)
	}
}

// A device that is merely idle is fine — dials happen on demand — so it must
// not be reported as broken. One with no address at all is the real problem.
func TestRunDistinguishesIdleFromUnknown(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{
		status:  lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/41001"}},
		devices: []lanternclient.Device{{PeerID: "p2", Alias: "idle", KnownAddresses: 4}},
	}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	if d := find(t, r, "paired-devices"); d.Status != Pass {
		t.Fatalf("an idle device with addresses should pass: %+v", d)
	}

	s.devices = []lanternclient.Device{{PeerID: "p2", Alias: "unknown", KnownAddresses: 0}}
	r = Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	if d := find(t, r, "paired-devices"); d.Status != Warn || !contains(d.Detail, "no known address") {
		t.Fatalf("a device with no address must warn: %+v", d)
	}
}

// No pairings at all is a warning, not a failure: one device is a valid
// single-device setup.
func TestRunWarnsWhenNothingIsPaired(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{status: lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/41001"}}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	if !r.Healthy {
		t.Fatalf("an unpaired single device is not unhealthy: %+v", r.Checks)
	}
	if d := find(t, r, "paired-devices"); d.Status != Warn || !contains(d.Fix, "trust add") {
		t.Fatalf("paired-devices = %+v", d)
	}
}

// A missing data dir is a warning, not a failure: the daemon may legitimately
// run under --data-dir elsewhere, and it has already proved it has an identity
// by answering. The warning matters because a daemon that starts without an
// explicit --data-dir will mint a new identity and lose every pairing.
func TestRunWarnsAboutMissingDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "never-created"))
	s := &stubDaemon{status: lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/41001"}}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	c := find(t, r, "data-dir")
	if c.Status != Warn || !contains(c.Detail, "does not exist") {
		t.Fatalf("data-dir = %+v", c)
	}
	if !r.Healthy {
		t.Fatalf("a missing default data dir must not make the setup unhealthy: %+v", r.Checks)
	}
}

// mDNS is one discovery path among several, so a quiet UDP 5353 is a warning
// with a working alternative, never a failure.
func TestRunTreatsSilentMDNSAsWarning(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{status: lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/41001"}}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	c := find(t, r, "mdns")
	if c.Status == Fail {
		t.Fatalf("mDNS silence must not fail the report: %+v", c)
	}
	if c.Status == Warn && !contains(c.Fix, "--peer") && !contains(c.Fix, "--no-lan") {
		t.Fatalf("mDNS warning must offer a route around it: %+v", c)
	}
}

// The firewall check is Windows-specific and must skip elsewhere, rather than
// inventing a Linux rule to inspect.
func TestRunSkipsFirewallOffWindows(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{status: lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/192.168.1.11/tcp/41001"}}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	c := find(t, r, "firewall")
	if isWindows() {
		if c.Status != Warn {
			t.Fatalf("on Windows the firewall must be flagged: %+v", c)
		}
		return
	}
	if c.Status != Skip {
		t.Fatalf("off Windows the firewall check must skip: %+v", c)
	}
}

func TestIsRoutableAddr(t *testing.T) {
	routable := []string{
		"/ip4/192.168.1.11/tcp/41001",
		"/ip4/10.0.0.5/tcp/1",
		"/ip6/2001:db8::1/tcp/1",
	}
	for _, a := range routable {
		if !isRoutableAddr(a) {
			t.Errorf("isRoutableAddr(%q) = false, want true", a)
		}
	}
	notRoutable := []string{
		"/ip4/127.0.0.1/tcp/41001",
		"/ip4/0.0.0.0/tcp/41001",
		// Link-local v4 is what WSL and Hyper-V hand out, and it reaches only
		// the local segment.
		"/ip4/169.254.146.165/tcp/59250",
		"/ip6/fe80::1/tcp/1",
		"/ip4/127.0.0.1/udp/41001/quic-v1",
		"",
		"not-a-multiaddr",
	}
	for _, a := range notRoutable {
		if isRoutableAddr(a) {
			t.Errorf("isRoutableAddr(%q) = true, want false", a)
		}
	}
}

func TestTCPAddr(t *testing.T) {
	host, port, ok := tcpAddr("/ip4/192.168.1.11/tcp/41001")
	if !ok || host != "192.168.1.11" || port != 41001 {
		t.Fatalf("tcpAddr = %q %d %v", host, port, ok)
	}
	if _, _, ok := tcpAddr("/ip4/192.168.1.11/udp/41001/quic-v1"); ok {
		t.Fatal("a UDP address has no TCP port")
	}
	if _, _, ok := tcpAddr("nonsense"); ok {
		t.Fatal("garbage has no port")
	}
	// A bare /tcp/N listen address carries no IP; loopback is the honest
	// reading and keeps it out of the routable set.
	if h, p, ok := tcpAddr("/tcp/41001"); !ok || h != "127.0.0.1" || p != 41001 {
		t.Fatalf("bare tcp addr = %q %d %v", h, p, ok)
	}
}

// The mDNS check must not bind-fail on a healthy machine, because the running
// daemon already holds UDP 5353 for its own service. Reporting that as a
// broken network is precisely the false alarm this command must not produce,
// so the probe binds an ephemeral port and a second probe must also open.
func TestListenMDNSIsReentrant(t *testing.T) {
	first, err := listenMDNS()
	if err != nil {
		t.Skipf("no multicast-capable interface in this environment: %v", err)
	}
	defer first.Close()
	second, err := listenMDNS()
	if err != nil {
		t.Fatalf("a second mDNS probe must open while one is held: %v", err)
	}
	defer second.Close()
}

// A query must be sendable and well-formed enough to elicit an answer; a
// malformed one is simply ignored, which would look identical to a blocked
// network.
func TestSendMDNSQuerySucceeds(t *testing.T) {
	conn, err := listenMDNS()
	if err != nil {
		t.Skipf("no multicast-capable interface in this environment: %v", err)
	}
	defer conn.Close()
	if err := sendMDNSQuery(conn); err != nil {
		t.Fatalf("mDNS query should be sendable: %v", err)
	}
}

// Every failing or warning check must carry the command that fixes it; that
// pairing is the reason the command exists.
func TestFailingChecksCarryAFix(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := &stubDaemon{
		status:  lanternclient.Status{PeerID: "p1", Addrs: []string{"/ip4/127.0.0.1/tcp/41001"}},
		devices: []lanternclient.Device{{PeerID: "p2", Alias: "unknown", KnownAddresses: 0}},
	}
	srv := s.start(t)
	defer srv.Close()

	r := Run(t.Context(), lanternclient.New(srv.URL, ""), Options{})
	for _, c := range r.Checks {
		if c.Status == Fail || c.Status == Warn {
			if c.Fix == "" {
				t.Errorf("check %q is %s with no fix: %+v", c.Name, c.Status, c)
			}
		}
	}
}

// osStat is only exercised for its not-exist path in these tests; this keeps
// the real implementation honest if it is ever called against a real path.
func TestOsStatReportsRealDirectories(t *testing.T) {
	dir := t.TempDir()
	ok, err := osStat(dir)
	if err != nil || !ok {
		t.Fatalf("osStat(%q) = %v %v", dir, ok, err)
	}
	missing := filepath.Join(dir, "nope")
	ok, err = osStat(missing)
	if err != nil || ok {
		t.Fatalf("osStat(%q) = %v %v", missing, ok, err)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func isWindows() bool { return runtime.GOOS == "windows" }
