package protocols

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestHTTPWireQueryEscaping(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{name: "space and unicode", raw: "a=x y&b=ü", want: "/p?a=x%20y&b=%C3%BC"},
		{name: "controls", raw: "a=\t\n\x7f", want: "/p?a=%09%0A%7F"},
		{name: "unsafe ASCII", raw: `a="<>\^` + "`" + `{|}`, want: "/p?a=%22%3C%3E%5C%5E%60%7B%7C%7D"},
		{name: "preserved bytes", raw: "a=%2f+%3D&b=c;d=e", want: "/p?a=%2f+%3D&b=c;d=e"},
		{name: "fragment", raw: "a=x y#not-sent", want: "/p?a=x%20y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.RequestURI
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			_, err := doHTTP(context.Background(), &spi.ProtocolContext{Client: srv.Client()}, &spi.Request{URL: srv.URL + "/p?" + tc.raw})
			if err != nil {
				t.Fatal(err)
			}
			if got := <-seen; got != tc.want {
				t.Fatalf("wire target = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHTTPWireQueryEscapingBeforeQueryMerge(t *testing.T) {
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.RequestURI
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	_, err := doHTTP(context.Background(), &spi.ProtocolContext{Client: srv.Client()}, &spi.Request{
		URL: srv.URL + "/p?q=a\tb", Query: map[string]string{"x": "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "/p?q=a%09b&x=y" {
		t.Fatalf("wire target = %q", got)
	}
}
