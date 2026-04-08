package main

import (
	"errors"
	"net/http"
	"testing"
)

func TestResolveAddr(t *testing.T) {
	got := resolveAddr(func(string) string { return "" })
	if got != ":8080" {
		t.Fatalf("default addr mismatch: %s", got)
	}
	got2 := resolveAddr(func(string) string { return ":18080" })
	if got2 != ":18080" {
		t.Fatalf("custom addr mismatch: %s", got2)
	}
}

func TestRunWith(t *testing.T) {
	okListen := func(*http.Server) error { return nil }
	if err := runWith(":0", http.NewServeMux(), okListen); err != nil {
		t.Fatalf("runWith nil error: %v", err)
	}

	closedListen := func(*http.Server) error { return http.ErrServerClosed }
	if err := runWith(":0", http.NewServeMux(), closedListen); err != nil {
		t.Fatalf("runWith server closed should be nil: %v", err)
	}

	want := errors.New("boom")
	failListen := func(*http.Server) error { return want }
	if err := runWith(":0", http.NewServeMux(), failListen); !errors.Is(err, want) {
		t.Fatalf("runWith unexpected error: %v", err)
	}
}
