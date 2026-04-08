package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"agent/infra/internal/httpapi"
)

func main() {
	addr := resolveAddr(os.Getenv)
	log.Printf("agent-infra listening on %s", addr)
	if err := runWith(addr, httpapi.NewRouter(), defaultListenAndServe); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func resolveAddr(getenv func(string) string) string {
	addr := getenv("AGENT_INFRA_ADDR")
	if addr == "" {
		return ":8080"
	}
	return addr
}

func runWith(addr string, handler http.Handler, listen func(*http.Server) error) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	err := listen(server)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func defaultListenAndServe(server *http.Server) error {
	return server.ListenAndServe()
}
