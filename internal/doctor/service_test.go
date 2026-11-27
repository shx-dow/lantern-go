package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/shx-dow/lantern-go/internal/service"
	"github.com/shx-dow/lantern-go/pkg/lanternclient"
)

// The service check is what turns "it worked yesterday" into a diagnosis: a
// foreground daemon stops with its terminal, and an installed service that is
// stopped will not come back after a reboot.

func TestCheckServiceNotInstalledWarnsWithTheInstallCommand(t *testing.T) {
	c := checkService(func() (service.Status, error) {
		return service.Status{Supported: true, Platform: "systemd"}, nil
	})
	if c.Status != Warn {
		t.Fatalf("status = %s, want warn: %+v", c.Status, c)
	}
	if c.Fix != "lantern service install" {
		t.Errorf("fix = %q, want the install command", c.Fix)
	}
}

func TestCheckServiceInstalledButStoppedFailsWithTheStartCommand(t *testing.T) {
	c := checkService(func() (service.Status, error) {
		return service.Status{
			Supported: true, Platform: "systemd", Installed: true,
			Detail: "unit installed but not running",
			Fix:    "systemctl --user start lanternd.service",
		}, nil
	})
	if c.Status != Fail {
		t.Fatalf("status = %s, want fail: %+v", c.Status, c)
	}
	if c.Fix != "systemctl --user start lanternd.service" {
		t.Errorf("fix = %q, want the platform start command", c.Fix)
	}
}

func TestCheckServiceRunningWithLingeringOffWarns(t *testing.T) {
	linger := false
	c := checkService(func() (service.Status, error) {
		return service.Status{
			Supported: true, Platform: "systemd", Installed: true, Running: true,
			Linger: &linger, Detail: "unit is active",
		}, nil
	})
	if c.Status != Warn {
		t.Fatalf("status = %s, want warn: %+v", c.Status, c)
	}
	if c.Fix != "loginctl enable-linger $USER" {
		t.Errorf("fix = %q, want the lingering command", c.Fix)
	}
}

func TestCheckServiceRunningAndLingeringPasses(t *testing.T) {
	linger := true
	c := checkService(func() (service.Status, error) {
		return service.Status{
			Supported: true, Platform: "systemd", Installed: true, Running: true,
			Linger: &linger, Detail: "unit is active",
		}, nil
	})
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %+v", c.Status, c)
	}
	if c.Fix != "" {
		t.Errorf("a passing check should have no fix, got %q", c.Fix)
	}
}

func TestCheckServiceUnsupportedSkips(t *testing.T) {
	c := checkService(func() (service.Status, error) {
		return service.Status{Platform: "plan9"}, nil
	})
	if c.Status != Skip {
		t.Fatalf("status = %s, want skip: %+v", c.Status, c)
	}
}

func TestCheckServiceReadErrorWarnsRatherThanFailingTheRun(t *testing.T) {
	c := checkService(func() (service.Status, error) {
		return service.Status{}, errors.New("systemctl not found")
	})
	if c.Status != Warn {
		t.Fatalf("status = %s, want warn: %+v", c.Status, c)
	}
	if c.Fix != "lantern service status" {
		t.Errorf("fix = %q, want the status command", c.Fix)
	}
}

// The check must run even when the daemon is unreachable: a stopped service is
// often the reason, and it is the one finding that names the fix.
func TestRunChecksTheServiceEvenWithoutADaemon(t *testing.T) {
	r := Run(context.Background(), lanternclient.New("http://127.0.0.1:1", ""), Options{
		ServiceStatus: func() (service.Status, error) {
			return service.Status{
				Supported: true, Platform: "systemd", Installed: true,
				Detail: "installed but not running",
				Fix:    "systemctl --user start lanternd.service",
			}, nil
		},
	})
	c := find(t, r, "service")
	if c.Status != Fail {
		t.Fatalf("service check = %+v, want fail", c)
	}
}

// A daemon on another machine says nothing about a service here, which is why
// the CLI leaves the provider nil for a non-loopback URL.
func TestRunOmitsTheServiceCheckWithoutAProvider(t *testing.T) {
	s := &stubDaemon{status: lanternclient.Status{
		PeerID: "p1", Addrs: []string{"/ip4/172.27.236.57/tcp/41001"},
	}}
	srv := s.start(t)
	defer srv.Close()

	r := Run(context.Background(), lanternclient.New(srv.URL, "tok"), Options{Token: "tok"})
	for _, c := range r.Checks {
		if c.Name == "service" {
			t.Fatalf("service check ran without a provider: %+v", c)
		}
	}
}
