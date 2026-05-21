package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/shx-dow/lantern-go/pkg/lantern"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: lantern send <path>")
		fmt.Println("       lantern receive <code> [output-dir]")
		os.Exit(1)
	}

	ln, err := lantern.New(lantern.Config{DataDir: "."})
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	ctx := context.Background()

	switch os.Args[1] {
	case "send":
		if len(os.Args) < 3 {
			log.Fatal("Usage: lantern send <path>")
		}
		peer, err := ln.Share(ctx, os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Sharing file. Code: %s\n", peer.Code)

	case "receive":
		if len(os.Args) < 3 {
			log.Fatal("Usage: lantern receive <code> [output-dir]")
		}
		outputDir := "."
		if len(os.Args) > 3 {
			outputDir = os.Args[3]
		}
		peer, err := ln.Receive(ctx, os.Args[2], outputDir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Received: %s\n", peer.FileName)

	default:
		log.Fatalf("Unknown command: %s", os.Args[1])
	}
}
