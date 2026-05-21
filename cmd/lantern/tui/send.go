package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

type dirEntry struct {
	name  string
	isDir bool
}

type sendModel struct {
	state       sendState
	lantern     *lantern.Lantern
	ctx         context.Context
	cancel      context.CancelFunc

	dir         string
	entries     []dirEntry
	cursor      int
	selected    string
	peer        *lantern.Peer

	progress    progress.Model
	bytes       int64
	total       int64
	fileName    string
	viewport    viewport.Model

	err      error
	done     bool
	shareCh  chan shareResult
}

type shareResult struct {
	peer *lantern.Peer
	err  error
}

func newSendModel(ln *lantern.Lantern) sendModel {
	ctx, cancel := context.WithCancel(context.Background())

	p := progress.New()
	p.ShowPercentage = true
	p.Width = 50

	vp := viewport.New(60, 5)

	m := sendModel{
		state:    sendPickFile,
		lantern:  ln,
		ctx:      ctx,
		cancel:   cancel,
		progress: p,
		viewport: vp,
		shareCh:  make(chan shareResult, 1),
	}
	m.readDir()
	return m
}

func (m *sendModel) readDir() {
	dir := m.dir
	if dir == "" {
		dir, _ = os.Getwd()
		m.dir = dir
	}
	ents, _ := os.ReadDir(dir)
	m.entries = nil
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m.entries = append(m.entries, dirEntry{name: e.Name(), isDir: e.IsDir()})
	}
	sort.Slice(m.entries, func(i, j int) bool {
		if m.entries[i].isDir != m.entries[j].isDir {
			return m.entries[i].isDir
		}
		return strings.ToLower(m.entries[i].name) < strings.ToLower(m.entries[j].name)
	})
	if m.cursor >= len(m.entries) {
		m.cursor = 0
	}
}

func (m sendModel) Init() tea.Cmd {
	return nil
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
		case "ctrl+c", "q":
			m.cancel()
			return m, tea.Quit
		case "esc":
			parent := filepath.Dir(m.dir)
			if parent != m.dir {
				m.dir = parent
				m.cursor = 0
				m.readDir()
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.entries) == 0 {
				break
			}
			e := m.entries[m.cursor]
			if e.isDir {
				m.dir = filepath.Join(m.dir, e.name)
				m.cursor = 0
				m.readDir()
			} else {
				m.selected = filepath.Join(m.dir, e.name)
				go func() {
					p, err := m.lantern.Share(m.ctx, m.selected)
					m.shareCh <- shareResult{peer: p, err: err}
				}()
				m.state = sendWaiting
				return m, nil
			}
		}
	}
	return m, nil
}

func (m sendModel) updateWaiting(msg tea.Msg) (tea.Model, tea.Cmd) {
	select {
	case r := <-m.shareCh:
		if r.err != nil {
			m.state = sendError
			m.err = r.err
			return m, nil
		}
		m.peer = r.peer
		return m, m.waitForEvents()
	default:
	}

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
		if m.state == sendWaiting && msg.bytes > 0 {
			m.state = sendTransferring
		}
		return m, m.waitForEvents()
	}

	return m, nil
}

func (m sendModel) waitForEvents() tea.Cmd {
	return func() tea.Msg {
		for {
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
				default:
					continue
				}
			case <-m.ctx.Done():
				return errMsg{m.ctx.Err()}
			}
		}
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
		s := titleStyle.Render("send a file") + "\n\n"
		s += infoStyle.Render(m.dir) + "\n\n"
		if len(m.entries) == 0 {
			s += "  (empty directory)" + "\n\n"
		} else {
			for i, e := range m.entries {
				cursor := "  "
				if i == m.cursor {
					cursor = "› "
				}
				icon := " "
				if e.isDir {
					icon = "D"
				}
				s += fmt.Sprintf("%s%s %s\n", cursor, icon, e.name)
			}
			s += "\n"
		}
		s += helpStyle.Render("↑/↓: navigate • enter: open/select • esc: up • q: quit")
		return appStyle.Render(s)

	case sendWaiting:
		if m.peer == nil {
			return appStyle.Render(
				titleStyle.Render("preparing...") + "\n\n" +
					infoStyle.Render("generating share code...") + "\n\n" +
					helpStyle.Render("esc: cancel"),
			)
		}
		code := m.peer.Code
		return appStyle.Render(
			titleStyle.Render("waiting for receiver") + "\n\n" +
				infoStyle.Render("share code:") + "\n" +
				codeStyle.Render(fmt.Sprintf("  %s  ", code)) + "\n\n" +
				infoStyle.Render("waiting for someone to connect with this code...") + "\n\n" +
				helpStyle.Render("esc: cancel"),
		)

	case sendTransferring:
		if m.total == 0 {
			return appStyle.Render(
				titleStyle.Render("sending...")+"\n\n"+
					infoStyle.Render("waiting for connection...")+"\n\n"+
					helpStyle.Render("esc: cancel"),
			)
		}
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
