package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type sendState int

const (
	sendPickFile sendState = iota
	sendGenerating
	sendWaiting
	sendTransferring
	sendDone
	sendError
)

type dirItem struct {
	name  string
	isDir bool
}

func (i dirItem) FilterValue() string { return i.name }

func (i dirItem) Title() string {
	if i.isDir {
		return "[dir] " + i.name
	}
	return i.name
}

func (i dirItem) Description() string {
	if i.isDir {
		return "directory"
	}
	return "file"
}

type sendModel struct {
	state    sendState
	lantern  *lantern.Lantern
	ctx      context.Context
	cancel   context.CancelFunc
	dir      string
	list     list.Model
	progress progress.Model
	spinner  spinner.Model
	help     help.Model
	keys     sendKeyMap
	peer     *lantern.Peer
	shareCh  chan shareResult

	selectedPath string
	bytes        int64
	total        int64
	fileName     string
	startedAt    time.Time
	finishedAt   time.Time
	err          error
	showHelp     bool
}

type sendKeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Open    key.Binding
	Back    key.Binding
	Cancel  key.Binding
	Dismiss key.Binding
	Quit    key.Binding
	Help    key.Binding
	Refresh key.Binding
}

func (k sendKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Open, k.Back, k.Cancel, k.Help}
}

func (k sendKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Open, k.Back},
		{k.Cancel, k.Dismiss, k.Help, k.Quit, k.Refresh},
	}
}

func defaultSendKeyMap() sendKeyMap {
	return sendKeyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open/select")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Cancel:  key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		Dismiss: key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("enter/esc", "close")),
		Quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	}
}

func newSendModel(ln *lantern.Lantern) sendModel {
	ctx, cancel := context.WithCancel(context.Background())
	items := loadSendItems(".")
	li := newSendList(items, 0, 0)

	return sendModel{
		state:    sendPickFile,
		lantern:  ln,
		ctx:      ctx,
		cancel:   cancel,
		dir:      ".",
		list:     li,
		progress: progress.New(progress.WithDefaultGradient(), progress.WithWidth(50)),
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
		help:     help.New(),
		keys:     defaultSendKeyMap(),
		shareCh:  make(chan shareResult, 1),
	}
}

func loadSendItems(dir string) []list.Item {
	ents, _ := os.ReadDir(dir)
	items := make([]list.Item, 0, len(ents))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		items = append(items, dirItem{name: e.Name(), isDir: e.IsDir()})
	}
	sort.Slice(items, func(i, j int) bool {
		ai := items[i].(dirItem)
		aj := items[j].(dirItem)
		if ai.isDir != aj.isDir {
			return ai.isDir
		}
		return strings.ToLower(ai.name) < strings.ToLower(aj.name)
	})
	return items
}

func newSendList(items []list.Item, width, height int) list.Model {
	d := list.NewDefaultDelegate()
	d.SetHeight(1)
	d.SetSpacing(0)
	d.ShowDescription = false
	l := list.New(items, d, width, height)
	l.Title = "send a file"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.Styles.Title = titleStyle
	l.Styles.PaginationStyle = helpStyle
	l.Styles.HelpStyle = helpStyle
	l.Styles.FilterPrompt = lipgloss.NewStyle().Foreground(subtle)
	return l
}

func (m sendModel) Init() tea.Cmd {
	if m.state == sendGenerating {
		return spinner.Tick
	}
	return nil
}

func (m sendModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.state {
	case sendPickFile:
		return m.updatePickFile(msg)
	case sendGenerating:
		return m.updateGenerating(msg)
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
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.cancel()
			return m, tea.Quit
		case key.Matches(msg, m.keys.Back):
			parent := filepath.Dir(m.dir)
			if parent != m.dir {
				m.dir = parent
				m.list = newSendList(loadSendItems(m.dir), m.list.Width(), m.list.Height())
			}
		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp
		case key.Matches(msg, m.keys.Refresh):
			m.list = newSendList(loadSendItems(m.dir), m.list.Width(), m.list.Height())
		case key.Matches(msg, m.keys.Open):
			if item, ok := m.list.SelectedItem().(dirItem); ok {
				if item.isDir {
					m.dir = filepath.Join(m.dir, item.name)
					m.list = newSendList(loadSendItems(m.dir), m.list.Width(), m.list.Height())
					return m, nil
				}
				m.selectedPath = filepath.Join(m.dir, item.name)
				m.state = sendGenerating
				m.spinner = spinner.New(spinner.WithSpinner(spinner.Dot))
				go func() {
					p, err := m.lantern.Share(m.ctx, m.selectedPath)
					m.shareCh <- shareResult{peer: p, err: err}
				}()
				return m, spinner.Tick
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m sendModel) updateGenerating(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.cancel()
			return m, tea.Quit
		}
	case shareResult:
		if msg.err != nil {
			m.state = sendError
			m.err = msg.err
			return m, nil
		}
		m.peer = msg.peer
		m.state = sendWaiting
		return m, m.waitForEvents()
	}

	select {
	case r := <-m.shareCh:
		return m.updateGenerating(r)
	default:
	}

	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
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
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
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
		if m.startedAt.IsZero() {
			m.startedAt = time.Now()
		}
		if msg.done {
			m.state = sendDone
			m.finishedAt = time.Now()
			return m, nil
		}
		if msg.bytes > 0 {
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
					return transferProgressMsg{fileName: e.FileName, bytes: e.Bytes, total: e.Total}
				case lantern.EventTransferDone:
					return transferProgressMsg{fileName: e.FileName, done: true}
				case lantern.EventError:
					return errMsg{e.Err}
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
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
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
		if m.startedAt.IsZero() {
			m.startedAt = time.Now()
		}
		if msg.done {
			m.state = sendDone
			m.finishedAt = time.Now()
			return m, nil
		}
		return m, m.waitForEvents()
	case progress.FrameMsg:
		pm, cmd := m.progress.Update(msg)
		m.progress = pm.(progress.Model)
		return m, cmd
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m sendModel) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Dismiss), key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m sendModel) updateError(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Dismiss), key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m sendModel) View() string {
	if m.list.Width() == 0 || m.list.Height() == 0 {
		m.list.SetSize(72, 18)
	}
	switch m.state {
	case sendPickFile:
		m.list.Title = "send a file"
		body := m.list.View()
		if m.showHelp {
			body += "\n" + m.help.View(m.keys)
		}
		return appStyle.Render(body)
	case sendGenerating:
		return appStyle.Render(
			titleStyle.Render("send a file") + "\n\n" +
				m.spinner.View() + " Generating share code...\n\n" +
				helpStyle.Render("esc: cancel"),
		)
	case sendWaiting:
		if m.peer == nil {
			return appStyle.Render(
				titleStyle.Render("generating share code...") + "\n\n" +
					m.spinner.View() + " waiting...\n\n" +
					helpStyle.Render("esc: cancel"),
			)
		}
		return appStyle.Render(
			titleStyle.Render("waiting for receiver") + "\n\n" +
			infoStyle.Render("share code:") + "\n" +
			codeStyle.Render(fmt.Sprintf("  %s  ", m.peer.Code)) + "\n\n" +
			infoStyle.Render("waiting for someone to connect with this code...") + "\n\n" +
			helpStyle.Render("esc: cancel"),
		)
	case sendTransferring:
		elapsed := time.Duration(0)
		if !m.startedAt.IsZero() {
			elapsed = time.Since(m.startedAt).Truncate(time.Second)
		}
		pct := 0.0
		if m.total > 0 {
			pct = float64(m.bytes) / float64(m.total)
		}
		return appStyle.Render(
			titleStyle.Render(fmt.Sprintf("sending: %s", m.fileName)) + "\n\n" +
				m.progress.ViewAs(pct) + "\n" +
				infoStyle.Render(fmt.Sprintf("%s / %s", formatBytes(m.bytes), formatBytes(m.total))) + "\n" +
				infoStyle.Render(fmt.Sprintf("elapsed: %s", elapsed)) + "\n\n" +
				helpStyle.Render("esc: cancel"),
		)
	case sendDone:
		elapsed := time.Duration(0)
		if !m.startedAt.IsZero() && !m.finishedAt.IsZero() {
			elapsed = m.finishedAt.Sub(m.startedAt).Truncate(time.Second)
		}
		return appStyle.Render(
			titleStyle.Render("sent!") + "\n\n" +
			infoStyle.Render(fmt.Sprintf("file: %s", m.fileName)) + "\n" +
			infoStyle.Render(fmt.Sprintf("size: %s", formatBytes(m.total))) + "\n" +
			infoStyle.Render(fmt.Sprintf("elapsed: %s", elapsed)) + "\n\n" +
			helpStyle.Render("enter: close • esc: quit"),
		)
	case sendError:
		return appStyle.Render(
			titleStyle.Render("error") + "\n\n" +
			errorStyle.Render(m.err.Error()) + "\n\n" +
			helpStyle.Render("enter: close • esc: quit"),
		)
	}
	return ""
}
