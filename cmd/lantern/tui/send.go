package tui

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type sendState int

const (
	sendPickFile sendState = iota
	sendWaiting
	sendTransferring
	sendDone
	sendError
)

type sendModel struct {
	state       sendState
	lantern     *lantern.Lantern
	ctx         context.Context
	cancel      context.CancelFunc

	fp          filepicker.Model
	selected    string
	peer        *lantern.Peer

	progress    progress.Model
	bytes       int64
	total       int64
	fileName    string
	viewport    viewport.Model

	err   error
	done  bool
}

func newSendModel(ln *lantern.Lantern) sendModel {
	fp := filepicker.New()
	fp.ShowHidden = false
	fp.FileAllowed = true
	fp.DirAllowed = false
	fp.CurrentDirectory, _ = os.Getwd()

	ctx, cancel := context.WithCancel(context.Background())

	p := progress.New()
	p.ShowPercentage = true
	p.Width = 50

	vp := viewport.New(60, 5)

	return sendModel{
		state:    sendPickFile,
		lantern:  ln,
		ctx:      ctx,
		cancel:   cancel,
		fp:       fp,
		progress: p,
		viewport: vp,
	}
}

func (m sendModel) Init() tea.Cmd {
	return m.fp.Init()
}

func (m sendModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.state {
	case sendPickFile:
		return m.updatePickFile(msg)
	case sendWaiting:
		return m.updateWaiting(msg)
	case sendTransferring:
		return m.updateTransferring(msg)
	case sendDone:
		return m.updateDone(msg)
	case sendError:
		return m.updateError(msg)
	}
	return m, nil
}

func (m sendModel) updatePickFile(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.fp.SetHeight(m.viewport.Height)
	}

	var cmd tea.Cmd
	m.fp, cmd = m.fp.Update(msg)

	if didSelect, path := m.fp.DidSelectFile(msg); didSelect {
		m.selected = path
		m.state = sendWaiting
		return m, m.startShare
	}

	return m, cmd
}

func (m sendModel) startShare() tea.Msg {
	peer, err := m.lantern.Share(m.ctx, m.selected)
	if err != nil {
		return errMsg{err}
	}
	return shareStartedMsg{peer: peer}
}

func (m sendModel) updateWaiting(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}

	case errMsg:
		m.state = sendError
		m.err = msg.err
		return m, nil

	case shareStartedMsg:
		m.peer = msg.peer
		m.state = sendWaiting
		return m, m.waitForEvents()

	case transferProgressMsg:
		m.bytes = msg.bytes
		m.total = msg.total
		m.fileName = msg.fileName
		if msg.done {
			m.state = sendDone
			m.done = true
			return m, nil
		}
		if m.state == sendWaiting && msg.bytes > 0 {
			m.state = sendTransferring
		}
		return m, m.waitForEvents()
	}

	return m, nil
}

func (m sendModel) waitForEvents() tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-m.lantern.Events():
			switch e.Type {
			case lantern.EventTransferProgress:
				return transferProgressMsg{
					fileName: e.FileName,
					bytes:    e.Bytes,
					total:    e.Total,
					done:     false,
				}
			case lantern.EventTransferDone:
				return transferProgressMsg{
					fileName: e.FileName,
					done:     true,
				}
			case lantern.EventError:
				return errMsg{e.Err}
			}
		case <-m.ctx.Done():
			return errMsg{m.ctx.Err()}
		}
		return nil
	}
}

func (m sendModel) updateTransferring(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}

	case errMsg:
		m.state = sendError
		m.err = msg.err
		return m, nil

	case transferProgressMsg:
		m.bytes = msg.bytes
		m.total = msg.total
		m.fileName = msg.fileName
		if msg.done {
			m.state = sendDone
			m.done = true
			return m, nil
		}
		return m, m.waitForEvents()

	case progress.FrameMsg:
		pm, cmd := m.progress.Update(msg)
		m.progress = pm.(progress.Model)
		return m, cmd
	}

	return m, nil
}

func (m sendModel) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m sendModel) updateError(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m sendModel) View() string {
	switch m.state {
	case sendPickFile:
		return appStyle.Render(
			titleStyle.Render("send a file") + "\n\n" +
				m.fp.View() + "\n\n" +
				helpStyle.Render("esc: back • enter: select • /: search"),
		)

	case sendWaiting:
		code := m.peer.Code
		return appStyle.Render(
			titleStyle.Render("waiting for receiver") + "\n\n" +
				infoStyle.Render("share code:") + "\n" +
				codeStyle.Render(fmt.Sprintf("  %s  ", code)) + "\n\n" +
				infoStyle.Render("waiting for someone to connect with this code...") + "\n\n" +
				helpStyle.Render("esc: cancel"),
		)

	case sendTransferring:
		pct := float64(m.bytes) / float64(m.total)
		return appStyle.Render(
			titleStyle.Render(fmt.Sprintf("sending: %s", m.fileName)) + "\n\n" +
				m.progress.ViewAs(pct) + "\n" +
				infoStyle.Render(fmt.Sprintf("%s / %s", formatBytes(m.bytes), formatBytes(m.total))) + "\n\n" +
				helpStyle.Render("esc: cancel"),
		)

	case sendDone:
		return appStyle.Render(
			titleStyle.Render("sent!") + "\n\n" +
				infoStyle.Render(fmt.Sprintf("file: %s", m.fileName)) + "\n" +
				infoStyle.Render(fmt.Sprintf("size: %s", formatBytes(m.total))) + "\n\n" +
				helpStyle.Render("enter: back • esc: quit"),
		)

	case sendError:
		return appStyle.Render(
			titleStyle.Render("error") + "\n\n" +
				errorStyle.Render(m.err.Error()) + "\n\n" +
				helpStyle.Render("enter: back • esc: quit"),
		)
	}
	return ""
}

type shareStartedMsg struct {
	peer *lantern.Peer
}

type errMsg struct {
	err error
}

type transferProgressMsg struct {
	fileName string
	bytes    int64
	total    int64
	done     bool
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
