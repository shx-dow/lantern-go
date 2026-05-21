package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

func main() {
	port := "4001"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", port),
			fmt.Sprintf("/ip4/0.0.0.0/udp/%s/quic-v1", port),
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	d, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	if err := d.Bootstrap(ctx); err != nil {
		log.Printf("bootstrap: %v", err)
	}

	_, err = relay.New(h, relay.WithResources(relay.Resources{
		MaxReservations: 256,
		MaxCircuits:     512,
	}))
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("relay peer id: %s\n", h.ID())
	for _, addr := range h.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, h.ID())
	}
	fmt.Println()
	fmt.Println("advertise this address to your lantern peers:")
	fmt.Println()
	fmt.Printf("  --relay %s/p2p/%s\n", h.Addrs()[0], h.ID())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	fmt.Println("\nshutting down")
}
