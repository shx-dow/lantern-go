package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	logging "github.com/ipfs/go-log/v2"
	"github.com/shx-dow/lantern-go/cmd/lantern/tui"
	"github.com/shx-dow/lantern-go/pkg/lantern"
)

func main() {
	logging.SetAllLoggers(logging.LevelError)

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

	peer, err := ln.Share(ctx, args[2])
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("share code: %s\n", peer.Code)
	fmt.Println("waiting for receiver...")

	for {
		select {
		case e := <-ln.Events():
			switch e.Type {
			case lantern.EventTransferProgress:
				pct := float64(e.Bytes) / float64(e.Total) * 100
				fmt.Printf("\rprogress: %.1f%% (%s / %s)", pct, formatBytes(e.Bytes), formatBytes(e.Total))
			case lantern.EventTransferDone:
				fmt.Printf("\nsent %s\n", peer.FileName)
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

func runReceive(ctx context.Context, ln *lantern.Lantern, args []string) {
	if len(args) < 3 {
		log.Fatal("usage: lantern receive <code> [output-dir]")
	}
	outputDir := "."
	if len(args) > 3 {
		outputDir = args[3]
	}

	peer, err := ln.Receive(ctx, args[2], outputDir)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("connecting to %s...\n", peer.ID)

	for {
		select {
		case e := <-ln.Events():
			switch e.Type {
			case lantern.EventTransferProgress:
				pct := float64(e.Bytes) / float64(e.Total) * 100
				fmt.Printf("\rprogress: %.1f%% (%s / %s)", pct, formatBytes(e.Bytes), formatBytes(e.Total))
			case lantern.EventTransferDone:
				fmt.Printf("\nreceived: %s\n", e.FileName)
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
	<-sig
	fmt.Println("\ninterrupted")
	cancel()
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
