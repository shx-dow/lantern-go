// Package doctor runs preflight checks on a Lantern setup. Every failure it
// reports names the next command to run, because the reason this exists is
// that the failures themselves are silent: a dismissed firewall prompt, a
// multicast boundary that drops mDNS, and a device that is simply off all
// look identical from the outside.
package doctor

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/paths"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// Status is the outcome of one check.
type Status string

const (
	// Pass means the check succeeded.
	Pass Status = "pass"
	// Warn means something is off that Lantern works around.
	Warn Status = "warn"
	// Fail means the setup is broken, and Fix says what to do about it.
	Fail Status = "fail"
	// Skip means the check does not apply here (e.g. firewall on Linux).
	Skip Status = "skip"
)

// Check is one finding. Detail is the human-readable reason; Fix is the
// concrete next command, present whenever Status is not Pass or Skip.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the whole run.
type Report struct {
	Checks []Check `json:"checks"`
	// Healthy is true when nothing failed. Warnings do not make a setup
	// unhealthy: they describe a limitation Lantern routes around.
	Healthy bool `json:"healthy"`
}

// Failed reports whether any check failed.
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Options configures a run.
type Options struct {
	// Token is the daemon bearer token; empty means "not supplied", which is
	// itself worth reporting since every call will be refused.
	Token string
	// ProbePeers dials each paired device to test reachability rather than
	// reporting only whether a connection is currently open.
	ProbePeers bool
}

// mdnsListenWindow is how long the mDNS check listens for an answer. It is
// short because silence is the normal case and says nothing on its own; the
// check exists to detect that multicast cannot leave the host at all.
const mdnsListenWindow = 2 * time.Second

// Run performs the checks against the daemon at base (host:port). It never
// returns an error: a broken setup is the expected input, and every failure
// becomes a Check instead.
func Run(ctx context.Context, client *lanternclient.Client, opts Options) Report {
	r := Report{Healthy: true}
	add := func(c Check) {
		r.Checks = append(r.Checks, c)
		if c.Status == Fail {
			r.Healthy = false
		}
	}

	add(checkBinary())
	add(checkToken(opts.Token))
	add(checkDataDir())

	host := clientHost(client)

	// The daemon check gates the rest: without it every later call fails for
	// the same single reason, and saying so once is clearer than five
	// identical errors.
	status, err := client.Status()
	if err != nil {
		add(daemonCheck(host, err))
		return r
	}
	add(Check{
		Name:   "daemon",
		Status: Pass,
		Detail: fmt.Sprintf("reachable at %s as %s (%s), lan_only=%v",
			host, orDash(status.DeviceName), orDash(status.PeerID), status.LANOnly),
	})

	add(checkListenAddrs(status.Addrs))
	add(checkP2PPortPinned(status))
	add(checkMDNS(ctx, status.Addrs))
	add(checkFirewall(status.Addrs))
	add(checkDevices(ctx, client, opts.ProbePeers))
	return r
}

// checkBinary reports the running build. An old binary is a common source of
// confusion after a flag was added: the daemon runs an old build while the
// operator's shell has a new one.
func checkBinary() Check {
	return Check{
		Name:   "binary",
		Status: Pass,
		Detail: fmt.Sprintf("lantern %s on %s/%s", versionString(), runtime.GOOS, runtime.GOARCH),
	}
}

// checkToken reports whether a token was supplied, since without one every
// API call is refused with 401 and the operator would otherwise see only a
// generic auth failure.
func checkToken(token string) Check {
	if strings.TrimSpace(token) == "" {
		return Check{
			Name:   "token",
			Status: Fail,
			Detail: "no bearer token supplied, so every daemon call will be refused",
			Fix: fmt.Sprintf("export LANTERN_DAEMON_TOKEN=$(cat %s)   # or pass --daemon-token",
				lanternclientTokenPath()),
		}
	}
	return Check{Name: "token", Status: Pass, Detail: "bearer token supplied"}
}

// checkDataDir reports where identity and pairings live, and whether that
// directory is present.
//
// A missing directory is only a warning, never a failure: the daemon may
// legitimately run under --data-dir somewhere else, and it has already proven
// it has an identity by answering at all. It matters because it is where the
// identity key lives, so its absence on a machine that is not running is how
// a device silently loses its pairings after a reboot.
func checkDataDir() Check {
	dir := paths.Data()
	exists, err := osStat(dir)
	switch {
	case err != nil:
		return Check{
			Name:   "data-dir",
			Status: Warn,
			Detail: fmt.Sprintf("%s is not readable: %v", dir, err),
			Fix:    "lantern daemon --data-dir " + dir,
		}
	case !exists:
		return Check{
			Name:   "data-dir",
			Status: Warn,
			Detail: fmt.Sprintf("%s does not exist, so a daemon run without --data-dir will mint a new identity and lose every pairing", dir),
			Fix:    "lantern daemon --data-dir " + dir,
		}
	default:
		return Check{
			Name:   "data-dir",
			Status: Pass,
			Detail: dir + " (identity, pairings, token live here)",
		}
	}
}

// daemonCheck turns a failed status call into a diagnosis. Connection refused
// and 401 are different problems and need different fixes, which is why
// APIError carries the status code.
func daemonCheck(host string, err error) Check {
	var api *lanternclient.APIError
	if asAPIError(err, &api) {
		switch api.Code {
		case 401:
			return Check{
				Name:   "daemon",
				Status: Fail,
				Detail: fmt.Sprintf("daemon at %s refused the token (401)", host),
				Fix: fmt.Sprintf("export LANTERN_DAEMON_TOKEN=$(cat %s)   # the token the daemon printed on startup",
					lanternclientTokenPath()),
			}
		default:
			return Check{
				Name:   "daemon",
				Status: Fail,
				Detail: fmt.Sprintf("daemon at %s returned %d: %s", host, api.Code, api.Message),
				Fix:    "lantern daemon",
			}
		}
	}
	return Check{
		Name:   "daemon",
		Status: Fail,
		Detail: fmt.Sprintf("no daemon answering at %s: %v", host, err),
		Fix:    "lantern daemon",
	}
}

// checkListenAddrs separates bindable addresses from ones another machine can
// actually reach. Loopback on the listen side is normal; loopback advertised
// to peers is not, and only loopback plus link-local means nothing is
// dialable from elsewhere on the network.
func checkListenAddrs(addrs []string) Check {
	var routable []string
	for _, a := range addrs {
		if isRoutableAddr(a) {
			routable = append(routable, a)
		}
	}
	if len(addrs) == 0 {
		return Check{
			Name:   "listen-addresses",
			Status: Fail,
			Detail: "the daemon reports no libp2p listen addresses",
			Fix:    "lantern daemon --p2p-port 41001",
		}
	}
	if len(routable) == 0 {
		return Check{
			Name:   "listen-addresses",
			Status: Fail,
			Detail: fmt.Sprintf("only loopback or link-local addresses are advertised (%s), so no other device can dial this one", strings.Join(addrs, ", ")),
			Fix:    "lantern daemon --p2p-port 41001   # a fixed port, so --peer addresses stay valid across restarts",
		}
	}
	return Check{
		Name:   "listen-addresses",
		Status: Pass,
		Detail: fmt.Sprintf("%d routable of %d advertised: %s", len(routable), len(addrs), strings.Join(routable, ", ")),
	}
}

// checkP2PPortPinned warns about the failure mode that costs an afternoon:
// with a random libp2p port, any --peer address recorded for this device
// stops being valid at the next restart, and the symptom looks like the other
// side going offline.
//
// It reads the daemon's own reported p2p_port rather than inferring from an
// address: any single observed port looks plausible, so guessing would either
// miss the problem or cry wolf on a deliberate choice of port.
func checkP2PPortPinned(status lanternclient.Status) Check {
	observed := 0
	for _, a := range status.Addrs {
		if _, port, ok := tcpAddr(a); ok {
			observed = port
			break
		}
	}
	if status.P2PPort == 0 {
		return Check{
			Name:   "p2p-port",
			Status: Warn,
			Detail: fmt.Sprintf("the daemon was started without --p2p-port, so its listen port is random (%d this time) and a --peer address pointing at this device breaks on restart", observed),
			Fix:    "lantern daemon --p2p-port 41001   # then record the new address on the other device with --peer",
		}
	}
	return Check{
		Name:   "p2p-port",
		Status: Pass,
		Detail: fmt.Sprintf("libp2p port pinned at %d, so --peer addresses stay valid across restarts", status.P2PPort),
	}
}

// checkMDNS tests whether mDNS announcements can leave this host at all.
//
// It deliberately does not ask whether a peer answered. A silent two-second
// window is the expected result on a single-device machine and on any network
// where nothing else runs Lantern, so "no response" says almost nothing. What
// is worth knowing is whether the multicast socket can be opened and a query
// can be sent, which is the step WSL2 NAT, Docker bridges, and VPN interfaces
// break: the packets never leave, and it looks identical to a device that is
// switched off.
func checkMDNS(ctx context.Context, addrs []string) Check {
	if len(addrs) == 0 {
		return Check{Name: "mdns", Status: Skip, Detail: "no addresses to announce"}
	}

	conn, err := listenMDNS()
	if err != nil {
		return Check{
			Name:   "mdns",
			Status: Warn,
			Detail: fmt.Sprintf("cannot open the multicast socket on UDP 5353: %v", err),
			Fix:    "lantern daemon --peer /ip4/<other-device-ip>/tcp/<port>/p2p/<peer-id>   # static peers skip discovery entirely",
		}
	}
	defer conn.Close()

	listenCtx, cancel := context.WithTimeout(ctx, mdnsListenWindow)
	defer cancel()
	go drainMDNS(conn, listenCtx)

	if err := sendMDNSQuery(conn); err != nil {
		return Check{
			Name:   "mdns",
			Status: Warn,
			Detail: fmt.Sprintf("the multicast socket is open but a query could not be sent: %v", err),
			Fix:    "lantern daemon --peer /ip4/<other-device-ip>/tcp/<port>/p2p/<peer-id>",
		}
	}

	// Nothing answered in the window. That is normal, so this is a Warn with
	// the routes around it rather than a Fail — mDNS is one discovery path
	// and --peer or the DHT cover the gap.
	return Check{
		Name:   "mdns",
		Status: Warn,
		Detail: fmt.Sprintf("multicast works locally, but no Lantern answered within %s; if devices on the same network never appear, multicast is being dropped (WSL2 NAT, Docker, VPN, or corporate Wi-Fi)", mdnsListenWindow),
		Fix:    "lantern daemon --peer /ip4/<other-device-ip>/tcp/<port>/p2p/<peer-id>   # or --no-lan to use the public DHT",
	}
}

// checkFirewall warns on Windows, where the first daemon start raises a
// firewall prompt that silences all inbound discovery if dismissed. Linux and
// macOS need no equivalent, so the check skips rather than inventing a rule
// to inspect.
func checkFirewall(addrs []string) Check {
	if runtime.GOOS != "windows" {
		return Check{
			Name:   "firewall",
			Status: Skip,
			Detail: "not applicable on " + runtime.GOOS,
		}
	}
	var ports []string
	for _, a := range addrs {
		if _, port, ok := tcpAddr(a); ok {
			ports = append(ports, strconv.Itoa(port))
		}
	}
	return Check{
		Name:   "firewall",
		Status: Warn,
		Detail: fmt.Sprintf("on Windows, allow inbound TCP for port(s) %s on private networks; a dismissed prompt makes discovery fail silently", orDash(strings.Join(ports, ", "))),
		Fix:    "Windows Security > Firewall & network protection > Allow an app > lantern.exe (Private networks)",
	}
}

// checkDevices reports each paired device: whether it has any known address,
// whether it is connected, and — when probing — whether a dial succeeds.
func checkDevices(ctx context.Context, client *lanternclient.Client, probe bool) Check {
	devices, err := client.Devices(probe)
	if err != nil {
		return Check{
			Name:   "paired-devices",
			Status: Fail,
			Detail: fmt.Sprintf("cannot list paired devices: %v", err),
			Fix:    "lantern --daemon trust list",
		}
	}
	if len(devices) == 0 {
		return Check{
			Name:   "paired-devices",
			Status: Warn,
			Detail: "no devices are paired, so nothing can be read from or pushed to",
			Fix:    "lantern --daemon trust add <peer-id> <alias>   # run on both devices; trust is one-directional",
		}
	}

	bad := 0
	var details []string
	for _, d := range devices {
		name := orDash(d.Alias)
		switch {
		// Probed: the dial is the answer, and it is authoritative.
		case d.Reachable != nil && !*d.Reachable:
			bad++
			details = append(details, fmt.Sprintf("%s unreachable (%s)", name, orDash(d.Error)))
		case d.Reachable != nil:
			details = append(details, fmt.Sprintf("%s reachable", name))
		// Not probed: no address at all is the failure mode that matters. An
		// address with no live connection is not one, because connections
		// happen on demand and an idle device is the expected state.
		case d.KnownAddresses == 0:
			bad++
			details = append(details, fmt.Sprintf("%s has no known address yet", name))
		case d.Online:
			details = append(details, fmt.Sprintf("%s online", name))
		default:
			details = append(details, fmt.Sprintf("%s idle (%d addresses, dials on demand)", name, d.KnownAddresses))
		}
	}
	c := Check{
		Name:   "paired-devices",
		Status: Pass,
		Detail: strings.Join(details, "; "),
	}
	if bad > 0 {
		c.Status = Warn
		c.Fix = "lantern daemon --peer /ip4/<other-device-ip>/tcp/<port>/p2p/<peer-id>   # and confirm the other device's daemon is running"
	}
	return c
}

// tcpAddr extracts host and port from a /ip[46]/../tcp/N multiaddr. The host
// defaults to loopback when the multiaddr carries no IP component, which is
// the case for a bare /tcp/N listen address.
func tcpAddr(addr string) (host string, port int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(addr, "/"), "/")
	host = "127.0.0.1"
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "ip4", "ip6":
			host = parts[i+1]
		case "tcp":
			p, err := strconv.Atoi(parts[i+1])
			if err != nil {
				return "", 0, false
			}
			return host, p, true
		}
	}
	return "", 0, false
}

// isRoutableAddr reports whether an address is worth handing to another
// machine: not loopback, not unspecified, and not link-local, which by
// definition only reaches the local segment. IPv6 link-local (fe80::) counts
// as link-local too: it needs a scope and only works on one link.
func isRoutableAddr(addr string) bool {
	host, _, ok := tcpAddr(addr)
	if !ok {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return false
	}
	return true
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// clientHost recovers the host:port a client is pointed at, for messages.
func clientHost(c *lanternclient.Client) string {
	base := c.BaseURL()
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}
