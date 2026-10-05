package doctor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/shx-dow/lantern-go/internal/version"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// osStat reports dir's existence. It is a variable so tests can describe a
// machine that has never started a daemon.
var osStat = func(dir string) (exists bool, err error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

func versionString() string { return version.Get().Version }

func lanternclientTokenPath() string {
	return lanternclient.TokenPath()
}

// asAPIError is errors.As specialised to *APIError, kept separate so the
// intent reads clearly at each call site.
func asAPIError(err error, target **lanternclient.APIError) bool {
	return errors.As(err, target)
}

const (
	// mdnsPort is where mDNS traffic goes.
	mdnsPort = 5353
	// mdnsGroup is the all-hosts multicast group mDNS uses.
	mdnsGroup = "224.0.0.251"
)

// listenMDNS opens a socket joined to the mDNS group so the check can send a
// query and see whether anything answers.
//
// It deliberately binds an ephemeral port rather than 5353. The running daemon
// already holds 5353 for its own mDNS service, and on Linux binding the same
// unicast UDP port twice needs SO_REUSEPORT; without it the bind fails with
// "address already in use" and doctor would report a broken network on a
// healthy machine. A group member on any port still receives replies, because
// responders answer the group rather than the querier.
func listenMDNS() (net.PacketConn, error) {
	group := net.ParseIP(mdnsGroup)
	if group == nil {
		return nil, fmt.Errorf("bad mDNS group %q", mdnsGroup)
	}
	iface, err := multicastIface()
	if err != nil {
		return nil, err
	}
	if conn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: group}); err == nil {
		return conn, nil
	}
	// No usable multicast interface: fall back to a plain socket so the query
	// is still sent. Whether it leaves the host is exactly what the check is
	// trying to find out, so this is a valid fallback rather than a shortcut.
	return net.ListenPacket("udp4", ":0")
}

// multicastIface picks the interface a query would leave by: the first
// routable, multicast-capable, up interface.
func multicastIface() (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var fallback *net.Interface
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		// Loopback cannot carry multicast off the host, so it is only a
		// last resort when nothing else qualifies.
		if ifi.Flags&net.FlagLoopback != 0 {
			if fallback == nil {
				fallback = ifi
			}
			continue
		}
		return ifi, nil
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("no multicast-capable network interface is up")
}

// drainMDNS reads and discards multicast replies until ctx ends, so the
// probe's own socket does not fill up and so a response actually clears the
// kernel receive buffer.
func drainMDNS(conn net.PacketConn, ctx context.Context) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		if _, _, err := conn.ReadFrom(buf); err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				continue
			}
			return
		}
	}
}

// sendMDNSQuery sends one multicast query for the Lantern service, which is
// enough to tell whether an announcement can leave this host.
func sendMDNSQuery(conn net.PacketConn) error {
	// A minimal DNS query for PTR _lantern._tcp.local. Hand-built rather than
	// pulled from a library: the doctor must not gain a dependency to ask one
	// question, and a malformed query is answered with silence, not an error.
	var q []byte
	q = append(q, 0x00, 0x00)                         // transaction ID
	q = append(q, 0x00, 0x00)                         // standard query, no recursion
	q = append(q, 0x00, 0x01)                         // one question
	q = append(q, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00) // no answer/authority/additional
	for _, label := range strings.Split("_lantern._tcp.local", ".") {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0x00)
	q = append(q, 0x00, 0x0c) // QTYPE PTR
	q = append(q, 0x00, 0x01) // QCLASS IN

	dst := &net.UDPAddr{IP: net.ParseIP(mdnsGroup), Port: mdnsPort}
	_, err := conn.WriteTo(q, dst)
	return err
}
