package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// registerPath is the spike's presence endpoint. Same JSON shape as the
// multicast announce (App discriminates us), so either transport feeds the
// same Registry. Presence data only — no auth, like LocalSend's /register.
const registerPath = "/api/lantern/v1/register"

// NewRegisterHandler serves presence probes: it records the prober via
// onPeer and answers with self, completing two-way discovery in one
// round trip.
func NewRegisterHandler(self Device, onPeer func(Device)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var m message
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if m.App == "lantern" && m.Fingerprint != "" && m.Fingerprint != self.Fingerprint {
			onPeer(Device{
				Alias: m.Alias, Version: m.Version, Model: m.Model,
				Type: normType(m.Type), Fingerprint: m.Fingerprint,
				Port: m.Port, Addr: r.RemoteAddr,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(message{
			App: "lantern", Alias: self.Alias, Version: ProtoVersion,
			Model: self.Model, Type: self.Type, Fingerprint: self.Fingerprint,
			Port: self.Port, Announce: false,
		})
	})
}

// ServeRegister listens on TCP addr (same port as UDP discovery is fine —
// different protocol) and serves presence probes until ctx ends.
func ServeRegister(ctx context.Context, addr string, self Device, onPeer func(Device)) error {
	srv := &http.Server{Addr: addr, Handler: NewRegisterHandler(self, onPeer)}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Scan probes each ip on port with our presence and returns responders.
// Closed/filtered hosts are skipped after perProbe; parallel bounds the
// fan-out so a /24 finishes in seconds.
func Scan(ctx context.Context, self Device, port int, ips []net.IP, perProbe time.Duration, parallel int) []Device {
	if parallel < 1 {
		parallel = 16
	}
	jobs := make(chan net.IP)
	found := make(chan Device, len(ips))
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				if d, ok := probe(ctx, self, ip, port, perProbe); ok {
					select {
					case found <- d:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, ip := range ips {
			select {
			case jobs <- ip:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(found)
	}()
	var out []Device
	for d := range found {
		out = append(out, d)
	}
	return out
}

// probe sends one presence probe and parses the answer.
func probe(ctx context.Context, self Device, ip net.IP, port int, perProbe time.Duration) (Device, bool) {
	raw, err := json.Marshal(message{
		App: "lantern", Alias: self.Alias, Version: ProtoVersion,
		Model: self.Model, Type: self.Type, Fingerprint: self.Fingerprint,
		Port: self.Port, Announce: false,
	})
	if err != nil {
		return Device{}, false
	}
	pctx, cancel := context.WithTimeout(ctx, perProbe)
	defer cancel()
	url := fmt.Sprintf("http://%s%s", joinHostPort(ip.String(), port), registerPath)
	req, err := http.NewRequestWithContext(pctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Device{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Device{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Device{}, false
	}
	var m message
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&m); err != nil {
		return Device{}, false
	}
	if m.App != "lantern" || m.Fingerprint == "" || m.Fingerprint == self.Fingerprint {
		return Device{}, false
	}
	return Device{
		Alias: m.Alias, Version: m.Version, Model: m.Model,
		Type: normType(m.Type), Fingerprint: m.Fingerprint,
		Port: m.Port, Addr: ip.String(),
	}, true
}

// LocalSubnetIPs returns probe candidates from up, non-loopback IPv4
// interfaces. Prefixes wider than /24 are clamped to the local /24 so a
// /16 never turns into 65k probes.
func LocalSubnetIPs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	seen := map[string]bool{}
	add := func(ip net.IP) {
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
			return
		}
		if v4 := ip.To4(); v4 != nil {
			if k := v4.String(); !seen[k] {
				seen[k] = true
				out = append(out, v4)
			}
		}
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			if ones < 24 {
				ones = 24 // clamp wide prefixes to the local /24
			}
			base := ipToUint32(ip4.Mask(net.CIDRMask(ones, 32)))
			for i := uint32(0); i < (1 << (32 - ones)); i++ {
				add(uint32ToIP(base + i))
			}
		}
	}
	return out
}

func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uint32ToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

func normType(t DeviceType) DeviceType {
	switch t {
	case TypeMobile, TypeDesktop, TypeHeadless, TypeServer:
		return t
	default:
		return TypeDesktop
	}
}

func joinHostPort(host string, port int) string {
	return net.JoinHostPort(host, fmt.Sprint(port))
}
