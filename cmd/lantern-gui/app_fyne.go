//go:build fyne

package main

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// runFyne opens the Lantern window. All daemon work goes through svc; slow
// calls run in goroutines so the UI never freezes, and the Transfers tab
// refreshes on demand (Refresh button + after every action).
//
// Threading rule: every widget mutation goes through onUI (fyne.Do onto the
// main thread). Fyne's text shaper is shared and not goroutine-safe —
// concurrent Label.SetText from workers panics inside HarfbuzzShaper.Shape.
func runFyne(svc *GuiService) {
	a := app.NewWithID("com.lantern.app")
	w := a.NewWindow("Lantern")
	w.Resize(fyne.NewSize(720, 520))

	refreshTransfers, transfersTab := newTransfersTab(svc, w)
	refreshStatus, statusTab := newStatusTab(svc)

	refreshAll := func() {
		refreshTransfers()
		refreshStatus()
	}

	tabs := container.NewAppTabs(
		container.NewTabItem("Send", newSendTab(svc, w, refreshAll)),
		container.NewTabItem("Receive", newReceiveTab(svc, w, refreshAll)),
		container.NewTabItem("Transfers", transfersTab),
		container.NewTabItem("Status", statusTab),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	w.SetContent(tabs)
	refreshAll()
	w.ShowAndRun()
}

// onUI runs f on Fyne's main thread. Use it for every widget mutation made
// from a worker goroutine.
func onUI(f func()) {
	fyne.Do(f)
}

// newSendTab picks a local file and advertises it via POST /v1/shares.
// Native dialogs return real paths, so no upload round-trip is needed.
func newSendTab(svc *GuiService, w fyne.Window, refreshed func()) fyne.CanvasObject {
	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("Choose a file to share…")

	ttlEntry := widget.NewEntry()
	ttlEntry.SetPlaceHolder("Expiry seconds (0 = daemon default)")

	result := widget.NewLabel("")
	result.Wrapping = fyne.TextWrapWord

	status := widget.NewLabel("Idle")
	status.Wrapping = fyne.TextWrapWord

	share := func() {
		path := strings.TrimSpace(pathEntry.Text)
		if path == "" {
			dialog.ShowInformation("Nothing to share", "Pick a file first.", w)
			return
		}
		ttl, err := parseTTL(ttlEntry.Text)
		if err != nil {
			dialog.ShowError(fmt.Errorf("bad expiry: %w", err), w)
			return
		}
		status.SetText("Sharing…")
		go func() {
			rec, err := svc.ShareFileWithTTL(path, ttl)
			onUI(func() {
				if err != nil {
					status.SetText("Share failed")
					dialog.ShowError(err, w)
					return
				}
				result.SetText("Share code:\n" + rec.Code)
				status.SetText(fmt.Sprintf("Shared %s", displayName(rec.FileName, rec.ID)))
				refreshed()
			})
		}()
	}

	browse := widget.NewButton("Browse…", func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if r == nil {
				return // cancelled
			}
			_ = r.Close()
			pathEntry.SetText(r.URI().Path())
		}, w)
	})

	copyCode := widget.NewButton("Copy code", func() {
		code := lastLine(result.Text)
		if code == "" {
			return
		}
		w.Clipboard().SetContent(code)
		status.SetText("Code copied to clipboard")
	})

	shareBtn := widget.NewButton("Share", share)
	shareBtn.Importance = widget.HighImportance

	return container.NewVBox(
		widget.NewLabelWithStyle("Share a file", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		pathEntry,
		container.NewBorder(nil, nil, browse, nil, ttlEntry),
		shareBtn,
		widget.NewSeparator(),
		result,
		container.NewHBox(copyCode),
		widget.NewSeparator(),
		status,
	)
}

// newReceiveTab pulls a share code into a local directory via
// POST /v1/fetches.
func newReceiveTab(svc *GuiService, w fyne.Window, refreshed func()) fyne.CanvasObject {
	codeEntry := widget.NewEntry()
	codeEntry.SetPlaceHolder("Paste the 32-hex share code…")

	outEntry := widget.NewEntry()
	outEntry.SetPlaceHolder("Save to folder (default: current directory)")

	status := widget.NewLabel("Idle")
	status.Wrapping = fyne.TextWrapWord

	fetch := func() {
		code := strings.TrimSpace(codeEntry.Text)
		if code == "" {
			dialog.ShowInformation("Nothing to fetch", "Paste a share code first.", w)
			return
		}
		status.SetText("Fetching…")
		go func() {
			rec, err := svc.FetchCode(code, strings.TrimSpace(outEntry.Text))
			onUI(func() {
				if err != nil {
					status.SetText("Fetch failed")
					dialog.ShowError(err, w)
					return
				}
				status.SetText(fmt.Sprintf("Fetching %s (%s)", displayName(rec.FileName, rec.ID), rec.State))
				refreshed()
			})
		}()
	}

	browse := widget.NewButton("Browse…", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if uri == nil {
				return // cancelled
			}
			outEntry.SetText(uri.Path())
		}, w)
	})

	fetchBtn := widget.NewButton("Fetch", fetch)
	fetchBtn.Importance = widget.HighImportance

	return container.NewVBox(
		widget.NewLabelWithStyle("Receive a file", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		codeEntry,
		outEntry,
		container.NewHBox(browse, fetchBtn),
		widget.NewSeparator(),
		status,
	)
}

// newTransfersTab lists live transfers with progress and cancel buttons.
// It returns a refresh func plus the tab content.
func newTransfersTab(svc *GuiService, w fyne.Window) (func(), fyne.CanvasObject) {
	rows := container.NewVBox()
	scroll := container.NewVScroll(rows)
	scroll.SetMinSize(fyne.NewSize(680, 380))

	refresh := func() {
		go func() {
			live, liveErr := svc.ListTransfers("")
			hist, histErr := svc.ListHistory()
			onUI(func() {
				if liveErr != nil {
					rows.Objects = []fyne.CanvasObject{widget.NewLabel("Failed to load transfers: " + liveErr.Error())}
					rows.Refresh()
					return
				}
				if histErr != nil {
					hist = nil
				}
				rows.Objects = buildTransferRows(svc, w, live, hist)
				rows.Refresh()
			})
		}()
	}

	header := container.NewHBox(
		widget.NewLabelWithStyle("Transfers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		widget.NewButton("Refresh", refresh),
	)
	return refresh, container.NewBorder(header, nil, nil, nil, scroll)
}

// buildTransferRows renders live transfers (with progress + cancel) followed
// by recent history. Pure layout over already-fetched snapshots.
func buildTransferRows(svc *GuiService, w fyne.Window, live, hist []Transfer) []fyne.CanvasObject {
	var out []fyne.CanvasObject
	if len(live) == 0 {
		out = append(out, widget.NewLabel("No live transfers."))
	}
	for _, t := range live {
		t := t
		bar := widget.NewProgressBar()
		bar.SetValue(progress(t))
		head := widget.NewLabel(fmt.Sprintf("%s · %s · %s (%d/%d bytes)",
			t.Kind, displayName(t.FileName, t.ID), t.State, t.Bytes, t.Total))
		head.Wrapping = fyne.TextWrapWord
		cancel := widget.NewButton("Cancel", func() {
			go func() {
				if err := svc.CancelTransfer(t.ID); err != nil {
					err := err
					onUI(func() { dialog.ShowError(err, w) })
					return
				}
			}()
		})
		out = append(out, widget.NewCard("", "Code: "+t.Code, container.NewVBox(head, bar, cancel)))
	}
	if len(hist) > 0 {
		out = append(out, widget.NewSeparator(), widget.NewLabelWithStyle("Recent", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		for _, t := range hist {
			t := t
			out = append(out, widget.NewLabel(fmt.Sprintf("%s · %s · %s", t.Kind, displayName(t.FileName, t.ID), t.State)))
		}
	}
	return out
}

// newStatusTab shows daemon identity plus connected peers.
func newStatusTab(svc *GuiService) (func(), fyne.CanvasObject) {
	peerLabel := widget.NewLabel("—")
	peerLabel.Wrapping = fyne.TextWrapWord
	addrsLabel := widget.NewLabel("—")
	addrsLabel.Wrapping = fyne.TextWrapWord
	peersBox := container.NewVBox()

	refresh := func() {
		go func() {
			st, stErr := svc.GetStatus()
			peers, peersErr := svc.ListPeers()
			onUI(func() {
				if stErr != nil {
					peerLabel.SetText("Daemon unreachable: " + stErr.Error())
					return
				}
				peerLabel.SetText("Peer: " + st.PeerID)
				if len(st.Addrs) == 0 {
					addrsLabel.SetText("No listen addresses")
				} else {
					addrsLabel.SetText("Addrs: " + strings.Join(st.Addrs, ", "))
				}
				if peersErr != nil {
					peersBox.Objects = []fyne.CanvasObject{widget.NewLabel("Peers unavailable: " + peersErr.Error())}
				} else if len(peers) == 0 {
					peersBox.Objects = []fyne.CanvasObject{widget.NewLabel("No connected peers.")}
				} else {
					rows := make([]fyne.CanvasObject, 0, len(peers))
					for _, p := range peers {
						rows = append(rows, widget.NewLabel(p.ID+" ("+strings.Join(p.Addrs, ", ")+")"))
					}
					peersBox.Objects = rows
				}
				peersBox.Refresh()
			})
		}()
	}

	header := container.NewHBox(
		widget.NewLabelWithStyle("Daemon", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		widget.NewButton("Refresh", refresh),
	)
	body := container.NewVBox(peerLabel, addrsLabel, widget.NewSeparator(),
		widget.NewLabelWithStyle("Connected peers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), peersBox)
	return refresh, container.NewBorder(header, nil, nil, nil, container.NewVScroll(body))
}

// progress returns the 0..1 fraction for a transfer bar.
func progress(t Transfer) float64 {
	if t.Total <= 0 {
		return 0
	}
	v := float64(t.Bytes) / float64(t.Total)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// parseTTL accepts "" (daemon default) or a non-negative second count.
func parseTTL(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return n, nil
}

// displayName falls back to the transfer ID before metadata arrives.
func displayName(fileName, id string) string {
	if strings.TrimSpace(fileName) != "" {
		return fileName
	}
	return id
}

// lastLine picks the share code out of the multi-line result label.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
