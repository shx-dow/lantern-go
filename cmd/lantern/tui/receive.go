package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type receiveState int

const (
	recvInputCode receiveState = iota
	recvDiscovering
	recvTransferring
	recvDone
	recvError
)

type receiveModel struct {
	state      receiveState
	lantern    *lantern.Lantern
	ctx        context.Context
	cancel     context.CancelFunc
	input      textinput.Model
	session    *lantern.Session
	progress   progress.Model
	spinner    spinner.Model
	keys       receiveKeyMap
	bytes      int64
	total      int64
	fileName   string
	err        error
	outputDir  string
	receiveCh  chan sessionResult
	events     <-chan lantern.Event
	startedAt  time.Time
	finishedAt time.Time
	showHelp   bool
}

type receiveKeyMap struct {
	Submit  key.Binding
	Cancel  key.Binding
	Dismiss key.Binding
	Quit    key.Binding
	Help    key.Binding
	Refresh key.Binding
}

func defaultReceiveKeyMap() receiveKeyMap {
	return receiveKeyMap{
		Submit:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "connect")),
		Cancel:  key.NewBinding(key.WithKeys("ctrl+c", "esc"), key.WithHelp("esc/ctrl+c", "cancel")),
		Dismiss: key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("enter/esc", "close")),
		Quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "retry")),
	}
}

func newReceiveModel(ln *lantern.Lantern, outputDir string) receiveModel {
	ti := textinput.New()
	ti.Placeholder = "enter share code"
	ti.Focus()
	ti.CharLimit = 64
	ti.Width = 30

	ctx, cancel := context.WithCancel(context.Background())

	return receiveModel{
		state:     recvInputCode,
		lantern:   ln,
		ctx:       ctx,
		cancel:    cancel,
		input:     ti,
		progress:  progress.New(progress.WithDefaultGradient(), progress.WithWidth(50)),
		spinner:   spinner.New(spinner.WithSpinner(spinner.Dot)),
		keys:      defaultReceiveKeyMap(),
		outputDir: outputDir,
		receiveCh: make(chan sessionResult, 1),
	}
}

func (m *receiveModel) setSize(w, _ int) {
	if w > 0 {
		m.input.Width = contentWidth(w)
	}
}

func (m receiveModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m receiveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.state {
	case recvInputCode:
		return m.updateInputCode(msg)
	case recvDiscovering:
		return m.updateDiscovering(msg)
	case recvTransferring:
		return m.updateTransferring(msg)
	case recvDone:
		return m.updateDone(msg)
	case recvError:
		return m.updateError(msg)
	}
	return m, nil
}

func (m receiveModel) updateInputCode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.cancel()
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp
		case key.Matches(msg, m.keys.Refresh):
			m.input.SetValue("")
		case key.Matches(msg, m.keys.Submit):
			if m.input.Value() != "" {
				code := m.input.Value()
				m.state = recvDiscovering
				m.spinner = spinner.New(spinner.WithSpinner(spinner.Dot))
				go func() {
					s, _, err := m.lantern.ReceiveSession(m.ctx, code, m.outputDir)
					select {
					case m.receiveCh <- sessionResult{session: s, err: err}:
					case <-m.ctx.Done():
					}
				}()
				return m, spinner.Tick
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m receiveModel) updateDiscovering(msg tea.Msg) (tea.Model, tea.Cmd) {
	select {
	case r := <-m.receiveCh:
		if r.err != nil {
			m.state = recvError
			m.err = r.err
			return m, nil
		}
		m.session = r.session
		if m.session != nil {
			m.events = m.session.Events()
		}
		m.state = recvTransferring
		m.startedAt = time.Now()
		return m, waitForSessionEvents(m.events, m.ctx)
	default:
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.cancel()
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m receiveModel) updateTransferring(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.cancel()
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp
		}
	case errMsg:
		m.state = recvError
		m.err = msg.err
		if m.session != nil {
			m.session.Close()
		}
		return m, nil
	case transferProgressMsg:
		m.fileName = msg.fileName
		if msg.done {
			m.state = recvDone
			m.finishedAt = time.Now()
			if m.session != nil {
				m.session.Close()
			}
			return m, nil
		}
		m.bytes = msg.bytes
		m.total = msg.total
		return m, waitForSessionEvents(m.events, m.ctx)
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

func (m receiveModel) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Dismiss), key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m receiveModel) updateError(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Dismiss), key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m receiveModel) View() string {
	switch m.state {
	case recvInputCode:
		body := titleStyle.Render("receive a file") + "\n\n" +
			infoStyle.Render("enter the share code:") + "\n\n" +
			m.input.View()
		if m.showHelp {
			body += "\n\n" + helpStyle.Render("enter: confirm • esc: back • ctrl+c: quit")
		}
		return body
	case recvDiscovering:
		return titleStyle.Render("searching for peer...") + "\n\n" + m.spinner.View() + " finding peers to connect...\n\n" + helpStyle.Render("esc: cancel")
	case recvTransferring:
		return liveTransferView("receiving", m.fileName, m.bytes, m.total, liveElapsed(m.startedAt), m.progress)
	case recvDone:
		return doneTransferView("received!", m.fileName, m.total, doneElapsed(m.startedAt, m.finishedAt))
	case recvError:
		return errorTransferView(m.err)
	}
	return ""
}
