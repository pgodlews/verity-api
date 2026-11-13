package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/pgodlews/verity-api/plugins/grpc/internal/echo"
)

// countDials swaps the cache's dialer for one that counts, restoring it at
// the end of the test.
func countDials(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := conns.dial
	conns.dial = func(ctx context.Context, target string, creds credentials.TransportCredentials) (*grpc.ClientConn, error) {
		n.Add(1)
		return old(ctx, target, creds)
	}
	t.Cleanup(func() { conns.dial = old })
	return &n
}

// Steps against the same target share one connection and one reflection
// lookup instead of dialing and reflecting for every call.
func TestConnectionAndReflectionAreReused(t *testing.T) {
	dials := countDials(t)
	target := startEcho(t, echo.ReflectionBoth)
	for i := 0; i < 3; i++ {
		assertEcho(t, callEcho(t, context.Background(), target, "", map[string]any{"message": "again"}, nil), "again", nil)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dialed %d times for one target", got)
	}
	key := connKey(target, true, "")
	if reflected.get(key+"\x00"+echoService(t)+"\x00Echo") == nil {
		t.Fatal("reflection result was not cached")
	}

	// Another target gets its own connection.
	other := startEcho(t, echo.ReflectionBoth)
	assertEcho(t, callEcho(t, context.Background(), other, "", map[string]any{"message": "b"}, nil), "b", nil)
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2", got)
	}
}

func TestDescriptorSetIsParsedOnce(t *testing.T) {
	set := echo.DescriptorSet()
	a, err := cachedDescriptorSet(set)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cachedDescriptorSet(append([]byte(nil), set...))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("same descriptor set parsed twice")
	}
	if _, err := cachedDescriptorSet([]byte("not a descriptor set")); err == nil {
		t.Fatal("bad set accepted")
	}

	// A step still works from the cached set.
	target := startEcho(t, echo.ReflectionOff)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.pb"), set, 0o600); err != nil {
		t.Fatal(err)
	}
	resp := callEcho(t, context.Background(), target, dir, map[string]any{"message": "set"}, map[string]any{"descriptorSet": "echo.pb"})
	assertEcho(t, resp, "set", nil)
}

// Different TLS settings (here, the CA bundle) never share a connection key.
func TestConnKeyCoversTransportSettings(t *testing.T) {
	dir := t.TempDir()
	_, plainKey, err := transportCredsKeyed(dir, false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, k1, err := transportCredsKeyed(dir, false, false, map[string]any{"serverName": "a"})
	if err != nil {
		t.Fatal(err)
	}
	_, k2, err := transportCredsKeyed(dir, false, false, map[string]any{"serverName": "b"})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{
		connKey("t", true, plainKey): true,
		connKey("t", false, k1):      true,
		connKey("t", false, k2):      true,
		connKey("u", false, k2):      true,
	}
	if len(keys) != 4 {
		t.Fatalf("connection keys collide: %v", keys)
	}
}

func echoService(t *testing.T) string {
	t.Helper()
	svc, _, err := splitMethod(echo.MethodEcho)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
