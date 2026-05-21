package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
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
	state     receiveState
	lantern   *lantern.Lantern
	ctx       context.Context
	cancel    context.CancelFunc
	input     textinput.Model
	peer      *lantern.Peer
	progress  progress.Model
	bytes     int64
	total     int64
	fileName  string
	viewport  viewport.Model
	err       error
	outputDir string
}

func newReceiveModel(ln *lantern.Lantern, outputDir string) receiveModel {
	ti := textinput.New()
	ti.Placeholder = "enter share code"
	ti.Focus()
	ti.CharLimit = 16
	ti.Width = 30

	ctx, cancel := context.WithCancel(context.Background())

	p := progress.New()
	p.ShowPercentage = true
	p.Width = 50

	vp := viewport.New(60, 5)

	return receiveModel{
		state:     recvInputCode,
		lantern:   ln,
		ctx:       ctx,
		cancel:    cancel,
		input:     ti,
		progress:  p,
		viewport:  vp,
		outputDir: outputDir,
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
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		case "enter":
			if m.input.Value() != "" {
				m.state = recvDiscovering
				code := m.input.Value()
				return m, func() tea.Msg {
					peer, err := m.lantern.Receive(m.ctx, code, m.outputDir)
					if err != nil {
						return errMsg{err}
					}
					return receiveReadyMsg{peer: peer}
				}
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m receiveModel) updateDiscovering(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}

	case errMsg:
		m.state = recvError
		m.err = msg.err
		return m, nil

	case receiveReadyMsg:
		m.peer = msg.peer
		m.state = recvTransferring
		return m, m.waitForReceiveEvents()
	}

	return m, nil
}

func (m receiveModel) waitForReceiveEvents() tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-m.lantern.Events():
			switch e.Type {
			case lantern.EventTransferProgress:
				return transferProgressMsg{
					fileName: e.FileName,
					bytes:    e.Bytes,
					total:    e.Total,
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

func (m receiveModel) updateTransferring(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		}

	case errMsg:
		m.state = recvError
		m.err = msg.err
		return m, nil

	case transferProgressMsg:
		m.fileName = msg.fileName
		if msg.done {
			m.state = recvDone
			m.total = msg.total
			return m, nil
		}
		m.bytes = msg.bytes
		m.total = msg.total
		return m, m.waitForReceiveEvents()
	}

	return m, nil
}

func (m receiveModel) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m receiveModel) updateError(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m receiveModel) View() string {
	switch m.state {
	case recvInputCode:
		return appStyle.Render(
			titleStyle.Render("receive a file")+"\n\n"+
				infoStyle.Render("enter the share code:")+"\n"+
				m.input.View()+"\n\n"+
				helpStyle.Render("enter: confirm • esc: back • ctrl+c: quit"),
		)

	case recvDiscovering:
		return appStyle.Render(
			titleStyle.Render("searching for peer...")+"\n\n"+
				infoStyle.Render("looking for a peer with your code...")+"\n\n"+
				helpStyle.Render("esc: cancel"),
		)

	case recvTransferring:
		pct := float64(m.bytes) / float64(m.total)
		return appStyle.Render(
			titleStyle.Render(fmt.Sprintf("receiving: %s", m.fileName))+"\n\n"+
				m.progress.ViewAs(pct)+"\n"+
				infoStyle.Render(fmt.Sprintf("%s / %s", formatBytes(m.bytes), formatBytes(m.total)))+"\n\n"+
				helpStyle.Render("esc: cancel"),
		)

	case recvDone:
		return appStyle.Render(
			titleStyle.Render("received!")+"\n\n"+
				infoStyle.Render(fmt.Sprintf("file: %s", m.fileName))+"\n"+
				infoStyle.Render(fmt.Sprintf("size: %s", formatBytes(m.total)))+"\n\n"+
				helpStyle.Render("enter: back • esc: quit"),
		)

	case recvError:
		return appStyle.Render(
			titleStyle.Render("error")+"\n\n"+
				errorStyle.Render(m.err.Error())+"\n\n"+
				helpStyle.Render("enter: back • esc: quit"),
		)
	}
	return ""
}

type receiveReadyMsg struct {
	peer *lantern.Peer
}
