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
	"github.com/charmbracelet/bubbles/viewport"
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
	path  string
	name  string
	isDir bool
	size  int64
	mod   time.Time
}

func (i dirItem) FilterValue() string { return i.name }

func (i dirItem) Title() string {
	if i.isDir {
		return "📁 " + i.name
	}
	return "📄 " + i.name
}

func (i dirItem) Description() string {
	if i.isDir {
		return "directory"
	}
	return "file"
}

func (i dirItem) detailLines() []string {
	if i.isDir {
		return []string{
			"type: directory",
			fmt.Sprintf("name: %s", i.name),
			fmt.Sprintf("path: %s", i.path),
		}
	}
	return []string{
		"type: file",
		fmt.Sprintf("name: %s", i.name),
		fmt.Sprintf("size: %s", formatBytes(i.size)),
		fmt.Sprintf("modified: %s", i.mod.Format("2006-01-02 15:04")),
		fmt.Sprintf("path: %s", i.path),
	}
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
	session  *lantern.Session
	shareCh  chan shareResult
	events   <-chan lantern.Event
	viewport viewport.Model

	selectedPath string
	bytes        int64
	total        int64
	fileName     string
	startedAt    time.Time
	finishedAt   time.Time
	err          error
	showHelp     bool
	width        int
	height       int
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
		viewport: viewport.New(0, 0),
	}
}

func loadSendItems(dir string) []list.Item {
	ents, _ := os.ReadDir(dir)
	items := make([]list.Item, 0, len(ents))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, _ := e.Info()
		it := dirItem{name: e.Name(), isDir: e.IsDir(), path: filepath.Join(dir, e.Name())}
		if info != nil {
			it.size = info.Size()
			it.mod = info.ModTime()
		}
		items = append(items, it)
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
	d.Styles = list.NewDefaultItemStyles()
	d.Styles.NormalTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#D9DCCF"))
	d.Styles.SelectedTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(highlight).Bold(true).Padding(0, 1)
	d.Styles.NormalDesc = lipgloss.NewStyle().Foreground(subtle)
	d.Styles.SelectedDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	d.Styles.DimmedTitle = lipgloss.NewStyle().Foreground(subtle)
	d.Styles.DimmedDesc = lipgloss.NewStyle().Foreground(subtle)
	l := list.New(items, d, width, height)
	l.Title = "send a file"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetShowTitle(false)
	l.Styles.Title = titleStyle
	l.Styles.PaginationStyle = helpStyle
	l.Styles.HelpStyle = helpStyle
	l.Styles.FilterPrompt = lipgloss.NewStyle().Foreground(subtle)
	return l
}

func (m sendModel) renderPicker() string {
	items := m.list.Items()
	selected := -1
	if len(items) > 0 {
		selected = m.list.Index()
		if selected < 0 || selected >= len(items) {
			selected = 0
		}
	}
	leftWidth := m.width / 2
	if leftWidth < 36 {
		leftWidth = 36
	}
	rightWidth := m.width - leftWidth - 1
	if rightWidth < 24 {
		rightWidth = 24
	}
	leftHeight := m.height - 9
	if leftHeight < 12 {
		leftHeight = 12
	}
	panelWidth := func(w int) int {
		if w <= 0 {
			return 0
		}
		return w - 2
	}
	panelHeight := func(h int) int {
		if h <= 0 {
			return 0
		}
		return h - 2
	}
	listStyle := panelStyle.Width(panelWidth(leftWidth)).Height(panelHeight(leftHeight))
	detailStyle := panelStyle.Width(panelWidth(rightWidth)).Height(panelHeight(leftHeight))

	left := m.list.View()
	if len(items) == 0 {
		left = emptyPickerArt(leftWidth, leftHeight)
	}
	selectedLine := ""
	selectedPath := m.dir
	selectedName := filepath.Base(m.dir)
	selectedMeta := "dir"

	if len(items) > 0 && selected >= 0 {
		if it, ok := items[selected].(dirItem); ok {
			selectedPath = it.path
			selectedName = it.name
			if it.isDir {
				selectedMeta = "directory"
			} else {
				selectedMeta = formatBytes(it.size)
			}
			selectedLine = railStyle.Render("▌") + " " + headingStyle.Render(selectedName)
		}
	}

	m.viewport.Width = panelWidth(leftWidth)
	m.viewport.Height = panelHeight(leftHeight)
	m.viewport.SetContent(railStyle.Render("files") + "\n\n" + left + "\n\n" + selectedLine)
	header := titleStyle.Render("send a file")

	var detail strings.Builder
	detail.WriteString(headingStyle.Render("details"))
	detail.WriteString("\n\n")
	detail.WriteString(headingStyle.Render("preview"))
	detail.WriteString("\n")
	detail.WriteString(railStyle.Render(selectedMeta))
	detail.WriteString(" ")
	detail.WriteString(headingStyle.Render(selectedName))
	detail.WriteString("\n")
	detail.WriteString(infoStyle.Render(wrapLine(selectedPath, rightWidth-4)))
	detail.WriteString("\n\n")
	if selected >= 0 {
		if it, ok := items[selected].(dirItem); ok {
			for _, line := range it.detailLines() {
				if strings.HasPrefix(line, "path:") {
					detail.WriteString(infoStyle.Render(wrapLine(line, rightWidth-4)))
				} else {
					detail.WriteString(line)
				}
				detail.WriteString("\n")
			}
		}
	}
	if len(items) > 8 {
		detail.WriteString("\n")
		detail.WriteString(infoStyle.Render("more below"))
	}

	status := infoStyle.Render(fmt.Sprintf("%d items", len(items)))
	if len(items) > 0 {
		status = infoStyle.Render(fmt.Sprintf("selection %d/%d", selected+1, len(items)))
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		listStyle.Render(m.viewport.View()),
		" ",
		detailStyle.Render(detail.String()),
	)

	breadcrumb := breadcrumbPath(m.dir, m.width)
	if breadcrumb != "" {
		header += "  " + infoStyle.Render(breadcrumb)
	}

	helpLine := helpStyle.Render("↑/↓ move  enter open/select  esc back  r refresh  ? help  q quit")
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", status, helpLine)
}

func breadcrumbPath(dir string, width int) string {
	parts := strings.Split(filepath.Clean(dir), string(filepath.Separator))
	if len(parts) == 0 {
		return dir
	}
	crumbs := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			crumbs = append(crumbs, p)
		}
	}
	path := strings.Join(crumbs, " / ")
	if width > 0 && len(path) > width-6 {
		path = "... / " + crumbs[len(crumbs)-1]
	}
	return "path: " + path
}

func emptyPickerArt(width, height int) string {
	lines := []string{
		"┌───────────────┐",
		"│   empty dir   │",
		"└───────────────┘",
		"",
		"no files here",
		"",
		"use esc to go up",
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m *sendModel) setSize(w, h int) {
	m.width = w
	m.height = h
	if w > 0 && h > 0 {
		m.list.SetSize(w/2, h-8)
		m.viewport.Width = w / 2
		m.viewport.Height = h - 8
	}
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
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, nil
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
					m.list.ResetSelected()
					return m, nil
				}
				m.selectedPath = filepath.Join(m.dir, item.name)
				m.state = sendGenerating
				m.spinner = spinner.New(spinner.WithSpinner(spinner.Dot))
				go func() {
					s, p, err := m.lantern.ShareSession(m.ctx, m.selectedPath)
					m.shareCh <- shareResult{peer: p, session: s, err: err}
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
	select {
	case r := <-m.shareCh:
		if r.err != nil {
			m.state = sendError
			m.err = r.err
			return m, nil
		}
		m.peer = r.peer
		m.session = r.session
		if m.session != nil {
			m.events = m.session.Events()
		}
		m.state = sendWaiting
		return m, m.waitForEvents()
	default:
	}

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
		m.session = r.session
		if m.session != nil {
			m.events = m.session.Events()
		}
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
			if m.session != nil {
				m.session.Close()
			}
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
			case e, ok := <-m.events:
				if !ok {
					return errMsg{fmt.Errorf("event stream closed")}
				}
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
		if m.session != nil {
			m.session.Close()
		}
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
			if m.session != nil {
				m.session.Close()
			}
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
	switch m.state {
	case sendPickFile:
		return m.renderPicker()
	case sendGenerating:
		return titleStyle.Render("send a file") + "\n\n" + m.spinner.View() + " Generating share code...\n\n" + helpStyle.Render("esc: cancel")
	case sendWaiting:
		if m.peer == nil {
			return titleStyle.Render("generating share code...") + "\n\n" + m.spinner.View() + " waiting...\n\n" + helpStyle.Render("esc: cancel")
		}
		return titleStyle.Render("waiting for receiver") + "\n\n" + infoStyle.Render("share code:") + "\n" + codeStyle.Render(fmt.Sprintf("%s", m.peer.Code)) + "\n\n" + infoStyle.Render("waiting for someone to connect with this code...") + "\n\n" + helpStyle.Render("esc: cancel")
	case sendTransferring:
		elapsed := time.Duration(0)
		if !m.startedAt.IsZero() {
			elapsed = time.Since(m.startedAt).Truncate(time.Second)
		}
		pct := 0.0
		if m.total > 0 {
			pct = float64(m.bytes) / float64(m.total)
		}
		return titleStyle.Render(fmt.Sprintf("sending: %s", m.fileName)) + "\n\n" + m.progress.ViewAs(pct) + "\n" + infoStyle.Render(fmt.Sprintf("%s / %s", formatBytes(m.bytes), formatBytes(m.total))) + "\n" + infoStyle.Render(fmt.Sprintf("elapsed: %s", elapsed)) + "\n\n" + helpStyle.Render("esc: cancel")
	case sendDone:
		elapsed := time.Duration(0)
		if !m.startedAt.IsZero() && !m.finishedAt.IsZero() {
			elapsed = m.finishedAt.Sub(m.startedAt).Truncate(time.Second)
		}
		return titleStyle.Render("sent!") + "\n\n" + infoStyle.Render(fmt.Sprintf("file: %s", m.fileName)) + "\n" + infoStyle.Render(fmt.Sprintf("size: %s", formatBytes(m.total))) + "\n" + infoStyle.Render(fmt.Sprintf("elapsed: %s", elapsed)) + "\n\n" + helpStyle.Render("enter: close • esc: quit")
	case sendError:
		return titleStyle.Render("error") + "\n\n" + errorStyle.Render(m.err.Error()) + "\n\n" + helpStyle.Render("enter: close • esc: quit")
	}
	return ""
}
