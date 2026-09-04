package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type errMsg struct {
	err error
}

type transferProgressMsg struct {
	fileName string
	bytes    int64
	total    int64
	done     bool
}

type shareResult struct {
	peer    *lantern.Peer
	session *lantern.Session
	err     error
}

type receiveResult struct {
	peer    *lantern.Peer
	session *lantern.Session
	err     error
}

func contentWidth(w int) int {
	if w <= 0 {
		return 80
	}
	return w
}

func contentHeight(h int) int {
	if h <= 0 {
		return 20
	}
	return h
}

func screenCanvas(w, h int, body string) string {
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return lipgloss.Place(w, h, lipgloss.Left, lipgloss.Top, body)
}

func wrapLine(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	lineWidth := 0
	for _, r := range s {
		if r == '\n' {
			b.WriteRune(r)
			lineWidth = 0
			continue
		}
		runeWidth := lipgloss.Width(string(r))
		if lineWidth > 0 && lineWidth+runeWidth > width {
			b.WriteByte('\n')
			lineWidth = 0
		}
		b.WriteRune(r)
		lineWidth += runeWidth
	}
	return b.String()
}

type view int

const (
	mainMenu view = iota
	sendView
	receiveView
)

type model struct {
	current   view
	lantern   *lantern.Lantern
	send      sendModel
	receive   receiveModel
	outputDir string
	width     int
	height    int
	choice    int
	ready     bool
}

func New(ln *lantern.Lantern, outputDir string) tea.Model {
	return &model{
		current:   mainMenu,
		lantern:   ln,
		outputDir: outputDir,
	}
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		if m.current == sendView {
			m.send.setSize(msg.Width, msg.Height)
			updated, cmd := m.send.Update(msg)
			m.send = updated.(sendModel)
			return m, cmd
		}
		if m.current == receiveView {
			m.receive.setSize(msg.Width, msg.Height)
			updated, cmd := m.receive.Update(msg)
			m.receive = updated.(receiveModel)
			return m, cmd
		}
	}

	switch m.current {
	case mainMenu:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateMenu(key)
		}
	case sendView:
		updated, cmd := m.send.Update(msg)
		m.send = updated.(sendModel)
		if m.send.state == sendError {
			m.send = newSendModel(m.lantern)
			m.current = mainMenu
		}
		return m, cmd
	case receiveView:
		updated, cmd := m.receive.Update(msg)
		m.receive = updated.(receiveModel)
		if m.receive.state == recvError {
			m.receive = newReceiveModel(m.lantern, m.outputDir)
			m.current = mainMenu
		}
		return m, cmd
	}

	return m, nil
}

func (m *model) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "up", "k":
		if m.choice > 0 {
			m.choice--
		}
	case "down", "j":
		if m.choice < 2 {
			m.choice++
		}
	case "enter":
		switch m.choice {
		case 0:
			m.current = sendView
			m.send = newSendModel(m.lantern)
			m.send.setSize(m.width, m.height)
			return m, m.send.Init()
		case 1:
			m.current = receiveView
			m.receive = newReceiveModel(m.lantern, m.outputDir)
			m.receive.setSize(m.width, m.height)
			return m, m.receive.Init()
		case 2:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *model) View() string {
	if !m.ready {
		return screenCanvas(m.width, m.height, titleStyle.Render("lantern")+"\n\ninitializing...")
	}

	switch m.current {
	case mainMenu:
		return screenCanvas(m.width, m.height, m.menuView())
	case sendView:
		return screenCanvas(m.width, m.height, m.send.View())
	case receiveView:
		return screenCanvas(m.width, m.height, m.receive.View())
	}
	return ""
}

func (m *model) menuView() string {
	options := []string{"send a file", "receive a file", "quit"}

	s := titleStyle.Render("lantern") + "\n\n"
	s += infoStyle.Render("peer-to-peer file sharing") + "\n\n"

	for i, opt := range options {
		cursor := " "
		if m.choice == i {
			cursor = "›"
		}
		prefix := ""
		if m.choice == i {
			prefix = buttonStyle.Render(cursor + " " + opt)
		} else {
			prefix = cursor + " " + opt
		}
		s += prefix + "\n"
	}

	s += "\n" + helpStyle.Render("↑/↓ navigate  enter select  q quit")
	return s
}
