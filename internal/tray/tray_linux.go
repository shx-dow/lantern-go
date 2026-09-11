//go:build linux

package tray

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const (
	watcherBus  = "org.kde.StatusNotifierWatcher"
	watcherPath = "/StatusNotifierWatcher"
	itemPath    = "/StatusNotifierItem"
	itemIface   = "org.kde.StatusNotifierItem"
)

// Available reports whether a tray could show: a graphical session must be
// present. D-Bus reachability is checked in Run.
func Available() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// icon is one StatusNotifierItem pixmap frame, a(iiay) on the wire.
type icon struct {
	Width  int32
	Height int32
	Pixels []byte
}

// tooltip mirrors the (sa(iiay)ss) ToolTip property.
type tooltip struct {
	Name  string
	Icons []icon
	Title string
	Text  string
}

// item implements the StatusNotifierItem methods. Clicks (Activate and
// SecondaryActivate) open the web UI; no menu is exported in v1.
type item struct {
	url string
}

// Activate opens the UI (left click).
func (i *item) Activate(x, y int32) *dbus.Error {
	_ = openBrowser(i.url)
	return nil
}

// SecondaryActivate opens the UI as well (middle/right click).
func (i *item) SecondaryActivate(x, y int32) *dbus.Error {
	_ = openBrowser(i.url)
	return nil
}

// ContextMenu is a no-op: v1 exports no menu.
func (i *item) ContextMenu(x, y int32) *dbus.Error { return nil }

// Scroll is a no-op.
func (i *item) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// Run registers a StatusNotifierItem and blocks until Quit. It returns
// ErrNotSupported when no session bus or watcher exists (headless).
func Run(cfg Config) error {
	if stopped() {
		return nil
	}
	conn, err := dbus.SessionBus()
	if err != nil {
		return ErrNotSupported
	}
	defer conn.Close()

	service := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(service, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("tray: bus name taken: %w", err)
	}

	const iconSize = 24
	px := iconARGB(iconSize)
	frame := icon{Width: iconSize, Height: iconSize, Pixels: px}
	props := map[string]map[string]*prop.Prop{
		itemIface: {
			"Category":   {Value: "ApplicationStatus"},
			"Id":         {Value: "lantern"},
			"Title":      {Value: cfg.Title},
			"Status":     {Value: "Active"},
			"ItemIsMenu": {Value: false},
			"IconName":   {Value: ""},
			"IconPixmap": {Value: []icon{frame}},
			"ToolTip":    {Value: tooltip{Title: cfg.Title, Text: cfg.Title}},
		},
	}
	if _, err := prop.Export(conn, itemPath, props); err != nil {
		return fmt.Errorf("tray: export props: %w", err)
	}
	if err := conn.Export(&item{url: cfg.UIURL}, itemPath, itemIface); err != nil {
		return fmt.Errorf("tray: export methods: %w", err)
	}

	// Without a watcher nothing can display the icon; treat that as
	// unsupported so callers fall back to console mode.
	obj := conn.Object(watcherBus, watcherPath)
	if call := obj.Call(watcherBus+".RegisterStatusNotifierItem", 0, service); call.Err != nil {
		return ErrNotSupported
	}

	<-stopCh
	// No unregister call exists in the spec; dropping the connection
	// releases the bus name and the watcher removes the icon.
	return nil
}
