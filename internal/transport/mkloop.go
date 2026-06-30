//go:build ignore

// Command mkloop regenerates remote_loop.sh (the embedded, minified on-device loop)
// from the readable source remote_loop.src.sh. It is invoked by `go generate
// ./internal/transport` via the directive in remote_loop.go; the round-trip is
// checked by TestEmbeddedLoopMatchesSource, so a stale or hand-edited
// remote_loop.sh fails the suite.
package main

import (
	"log"
	"os"

	"github.com/lucasdaddiego/lp10/internal/transport/loopgen"
)

func main() {
	src, err := os.ReadFile("remote_loop.src.sh")
	if err != nil {
		log.Fatalf("mkloop: read source: %v", err)
	}
	if err := os.WriteFile("remote_loop.sh", []byte(loopgen.Minify(string(src))), 0o644); err != nil {
		log.Fatalf("mkloop: write embed: %v", err)
	}
}
