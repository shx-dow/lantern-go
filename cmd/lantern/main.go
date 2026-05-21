package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shx-dow/lantern-go/cmd/lantern/tui"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

func main() {
	ln, err := lantern.New(lantern.Config{DataDir: "."})
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

	go handleSignals(cancel)
	go printEvents(ctx, ln)

	switch os.Args[1] {
	case "send":
		if len(os.Args) < 3 {
			log.Fatal("usage: lantern send <path>")
		}
		peer, err := ln.Share(ctx, os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\nsent %s (code: %s)\n", peer.FileName, peer.Code)

	case "receive":
		if len(os.Args) < 3 {
			log.Fatal("usage: lantern receive <code> [output-dir]")
		}
		outputDir := "."
		if len(os.Args) > 3 {
			outputDir = os.Args[3]
		}
		peer, err := ln.Receive(ctx, os.Args[2], outputDir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\nreceived from %s\n", peer.ID)

	default:
		fmt.Println("usage: lantern [send|receive]")
		os.Exit(1)
	}
}

func handleSignals(cancel context.CancelFunc) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	fmt.Println("\ninterrupted")
	cancel()
}

func printEvents(ctx context.Context, ln *lantern.Lantern) {
	for {
		select {
		case e := <-ln.Events():
			switch e.Type {
			case lantern.EventPeerFound:
				fmt.Printf("share code: %s\n", e.Code)
				fmt.Println("waiting for receiver...")
			case lantern.EventPeerConnected:
				fmt.Printf("connected to peer: %s\n", e.PeerID)
			case lantern.EventTransferStarted:
				fmt.Println("transfer starting...")
			case lantern.EventTransferProgress:
				pct := float64(e.Bytes) / float64(e.Total) * 100
				fmt.Printf("\rprogress: %.1f%% (%d/%d bytes)", pct, e.Bytes, e.Total)
			case lantern.EventTransferDone:
				fmt.Printf("\nreceived: %s\n", e.FileName)
			case lantern.EventError:
				fmt.Fprintf(os.Stderr, "error: %v\n", e.Err)
			}
		case <-ctx.Done():
			return
		}
	}
}
