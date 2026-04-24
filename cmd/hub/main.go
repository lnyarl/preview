// Command hub is the Preview control plane entry point.
//
// Phase 0 scope: serve a plain-text "Hello Hub" response on GET / at :8080.
// Graceful shutdown, configuration loading, and real routes are introduced
// in later phases.
package main

import (
	"log"
	"net/http"
)

// helloHandler responds with "Hello Hub" as text/plain on GET /.
func helloHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write([]byte("Hello Hub\n")); err != nil {
		log.Printf("write response: %v", err)
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", helloHandler)

	addr := ":8080"
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
