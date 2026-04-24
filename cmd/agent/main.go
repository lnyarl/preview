// Command agent is the Preview data plane entry point.
//
// Phase 0 scope: log "Hello Agent" and exit with code 0. The real Agent
// (outbound WebSocket to Hub, Docker lifecycle, preview containers) is
// introduced in later phases.
package main

import "log"

func main() {
	log.Println("Hello Agent")
}
