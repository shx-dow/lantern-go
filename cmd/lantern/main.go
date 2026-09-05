package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shx-dow/lantern-go/cmd/lantern/tui"
	"github.com/shx-dow/lantern-go/internal/format"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

type mdnsFilter struct{}

func (mdnsFilter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[WARN] mdns:")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func init() {
	log.SetOutput(mdnsFilter{})
}

func main() {
	ln, err := lantern.New(lantern.Config{})
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	if len(os.Args) < 2 {
		p := tea.NewProgram(tui.New(ln, "."), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go handleSignal(cancel)

	switch os.Args[1] {
	case "send":
		runSend(ctx, ln, os.Args)
	case "receive":
		runReceive(ctx, ln, os.Args)
	default:
		fmt.Println("usage: lantern [send|receive]")
		os.Exit(1)
	}
}

func runSend(ctx context.Context, ln *lantern.Lantern, args []string) {
	if len(args) < 3 {
		log.Fatal("usage: lantern send <path>")
	}

	session, peer, err := ln.ShareSession(ctx, args[2])
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	fmt.Printf("share code: %s\n", peer.Code)
	fmt.Println("waiting for receiver...")

	runTransfer(ctx, session, "sent", peer.FileName)
}

func runReceive(ctx context.Context, ln *lantern.Lantern, args []string) {
	if len(args) < 3 {
		log.Fatal("usage: lantern receive <code> [output-dir]")
	}
	outputDir := "."
	if len(args) > 3 {
		outputDir = args[3]
	}

	session, peer, err := ln.ReceiveSession(ctx, args[2], outputDir)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	fmt.Printf("connecting to %s...\n", peer.ID)

	runTransfer(ctx, session, "received:", "")
}

// runTransfer renders session events until the transfer completes, fails,
// or the context is cancelled. doneVerb/doneName describe the success line
// ("sent <file>" for shares, "received: <file>" for receives).
func runTransfer(ctx context.Context, session *lantern.Session, doneVerb, doneName string) {
	for {
		select {
		case e, ok := <-session.Events():
			if !ok {
				return
			}
			switch e.Type {
			case lantern.EventTransferProgress:
				pct := progressPercent(e.Bytes, e.Total)
				fmt.Printf("\rprogress: %.1f%% (%s / %s)", pct, format.Bytes(e.Bytes), format.Bytes(e.Total))
			case lantern.EventTransferDone:
				name := doneName
				if name == "" {
					name = e.FileName
				}
				fmt.Printf("\n%s %s\n", doneVerb, name)
				return
			case lantern.EventError:
				fmt.Fprintf(os.Stderr, "\nerror: %v\n", e.Err)
				return
			}
		case <-ctx.Done():
			fmt.Println("\ncancelled")
			return
		}
	}
}

func handleSignal(cancel context.CancelFunc) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	<-sig
	fmt.Println("\ninterrupted")
	cancel()
}

func progressPercent(bytes, total int64) float64 {
	if total <= 0 {
		return 100
	}
	return format.Ratio(bytes, total) * 100
}
