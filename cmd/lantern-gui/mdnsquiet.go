package main

import (
	"bytes"
	"log"
	"os"
)

// mdnsFilter drops the noisy-but-harmless zeroconf multicast warnings
// (notably on Windows, where setting the multicast interface routinely
// fails while DHT discovery still works). Same filter as lanternd; kept
// local because each command is its own main package.
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
