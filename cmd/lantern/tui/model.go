package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

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

	case tea.KeyMsg:
		switch m.current {
		case mainMenu:
			return m.updateMenu(msg)
		case sendView:
			updated, cmd := m.send.Update(msg)
			m.send = updated.(sendModel)
			if m.send.state == sendDone || m.send.state == sendError {
				m.send = newSendModel(m.lantern)
				m.current = mainMenu
			}
			return m, cmd
		case receiveView:
			updated, cmd := m.receive.Update(msg)
			m.receive = updated.(receiveModel)
			if m.receive.state == recvDone || m.receive.state == recvError {
				m.receive = newReceiveModel(m.lantern, m.outputDir)
				m.current = mainMenu
			}
			return m, cmd
		}
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
			return m, m.send.Init()
		case 1:
			m.current = receiveView
			m.receive = newReceiveModel(m.lantern, m.outputDir)
			return m, m.receive.Init()
		case 2:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *model) View() string {
	if !m.ready {
		return "\n  initializing..."
	}

	switch m.current {
	case mainMenu:
		return m.menuView()
	case sendView:
		return m.send.View()
	case receiveView:
		return m.receive.View()
	}
	return ""
}

func (m *model) menuView() string {
	options := []string{"send a file", "receive a file", "quit"}

	s := appStyle.Render(titleStyle.Render("lantern") + "\n\n")
	s += "  peer-to-peer file sharing\n\n"

	for i, opt := range options {
		cursor := " "
		if m.choice == i {
			cursor = "›"
		}
		prefix := "  "
		if m.choice == i {
			prefix = buttonStyle.Render(cursor + " " + opt)
		} else {
			prefix = "  " + cursor + " " + opt
		}
		s += prefix + "\n"
	}

	s += "\n" + helpStyle.Render("↑/↓: navigate • enter: select • q: quit")
	return s
}
