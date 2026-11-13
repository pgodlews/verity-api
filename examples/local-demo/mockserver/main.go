// Command mockserver is a tiny local API used by the local-demo example so the
// demo project runs fully offline (no external dependencies). It is NOT part of
// the product — just a fixture for docs/examples and CI smoke tests.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address (port 0 picks a free port)")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Demo", "verity")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   42,
			"name": "widget",
			"tags": []string{"a", "b"},
		})
	})
	mux.HandleFunc("/secure", func(w http.ResponseWriter, r *http.Request) {
		// Requires a bearer token. The token is a secret and must be redacted
		// in all Verity output.
		if r.Header.Get("Authorization") != "Bearer super-secret-token-abc123456" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"user":"demo"}`))
	})
	mux.HandleFunc("/soap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>` +
			`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<soap:Body><AddResponse><Result>7</Result></AddResponse></soap:Body>` +
			`</soap:Envelope>`))
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	// Print the bound address so callers using port 0 can find it.
	log.Printf("mockserver listening on %s", ln.Addr())
	log.Fatal(http.Serve(ln, mux))
}
