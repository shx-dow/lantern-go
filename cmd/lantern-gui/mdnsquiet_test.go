package main

import (
	"io"
	"os"
	"testing"
)

func TestMdnsFilterDropsMulticastWarnings(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	var f mdnsFilter
	dropped := "2026/09/13 15:26:36 [WARN] mdns: Failed to set multicast interface: setsockopt\n"
	kept := "lantern-gui embedded daemon on http://127.0.0.1:53148\n"
	if n, err := f.Write([]byte(dropped)); err != nil || n != len(dropped) {
		t.Fatalf("drop: got %d, %v", n, err)
	}
	if n, err := f.Write([]byte(kept)); err != nil || n != len(kept) {
		t.Fatalf("passthrough: got %d, %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != kept {
		t.Fatalf("expected only the kept line, got %q", raw)
	}
}
