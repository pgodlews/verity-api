package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// The plugin process lives for one run (or one desktop session), so it keeps
// client connections and parsed descriptors instead of dialing, reflecting,
// or parsing again for every step. Keys never hold credentials in clear: a
// connection key is a hash of the target and the transport settings.

// maxCachedConns bounds the connection cache; the oldest entry is closed
// when a new target would exceed it.
const maxCachedConns = 64

// maxCachedFiles bounds each descriptor cache.
const maxCachedFiles = 256

type connCache struct {
	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
	order []string
	// dial is the dialer; tests replace it.
	dial func(context.Context, string, credentials.TransportCredentials) (*grpc.ClientConn, error)
}

func newConnCache() *connCache {
	return &connCache{conns: map[string]*grpc.ClientConn{}, dial: dial}
}

var conns = newConnCache()

// connKey identifies a target plus the transport settings that shape a
// connection (plaintext, TLS options, and the CA bundle's contents).
func connKey(target string, plaintext bool, tlsKey string) string {
	h := sha256.New()
	h.Write([]byte(target))
	h.Write([]byte{0})
	if plaintext {
		h.Write([]byte("plaintext"))
	}
	h.Write([]byte{0})
	h.Write([]byte(tlsKey))
	return hex.EncodeToString(h.Sum(nil))
}

// get returns a ready connection for key, reusing a cached one. A cached
// connection that is shutting down is replaced; one in transient failure
// retries now instead of waiting out its reconnect backoff.
func (c *connCache) get(ctx context.Context, key, target string, creds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	c.mu.Lock()
	conn := c.conns[key]
	c.mu.Unlock()
	if conn != nil {
		switch conn.GetState() {
		case connectivity.Shutdown:
			c.drop(key, conn)
		case connectivity.TransientFailure:
			conn.ResetConnectBackoff()
			fallthrough
		default:
			if err := waitReady(ctx, conn); err != nil {
				return nil, err
			}
			return conn, nil
		}
	}
	conn, err := c.dial(ctx, target, creds)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.conns[key]; existing != nil {
		// Another call dialed the same target first; keep one connection.
		_ = conn.Close()
		return existing, nil
	}
	if len(c.order) >= maxCachedConns {
		oldest := c.order[0]
		c.order = c.order[1:]
		if old := c.conns[oldest]; old != nil {
			_ = old.Close()
		}
		delete(c.conns, oldest)
	}
	c.conns[key] = conn
	c.order = append(c.order, key)
	return conn, nil
}

func (c *connCache) drop(key string, conn *grpc.ClientConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conns[key] != conn {
		return
	}
	delete(c.conns, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i:i], c.order[i+1:]...)
			break
		}
	}
	_ = conn.Close()
}

// len reports how many connections are cached (for tests).
func (c *connCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.conns)
}

// filesCache keeps parsed descriptor files by key: a hash of a descriptor
// set's bytes, or a connection key plus the reflected symbol.
type filesCache struct {
	mu    sync.Mutex
	files map[string]*protoregistry.Files
	order []string
}

func newFilesCache() *filesCache {
	return &filesCache{files: map[string]*protoregistry.Files{}}
}

var (
	descriptorSets = newFilesCache()
	reflected      = newFilesCache()
)

func (c *filesCache) get(key string) *protoregistry.Files {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.files[key]
}

func (c *filesCache) put(key string, f *protoregistry.Files) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.files[key]; ok {
		return
	}
	if len(c.order) >= maxCachedFiles {
		delete(c.files, c.order[0])
		c.order = c.order[1:]
	}
	c.files[key] = f
	c.order = append(c.order, key)
}

// cachedDescriptorSet parses a descriptor set once per distinct content.
func cachedDescriptorSet(set []byte) (*protoregistry.Files, error) {
	sum := sha256.Sum256(set)
	key := hex.EncodeToString(sum[:])
	if f := descriptorSets.get(key); f != nil {
		return f, nil
	}
	f, err := filesFromDescriptorSet(set)
	if err != nil {
		return nil, err
	}
	descriptorSets.put(key, f)
	return f, nil
}

// cachedReflect runs server reflection once per connection key and symbol.
func cachedReflect(ctx context.Context, key string, conn *grpc.ClientConn, service, method string) (*protoregistry.Files, error) {
	rkey := key + "\x00" + service + "\x00" + method
	if f := reflected.get(rkey); f != nil {
		return f, nil
	}
	f, err := reflectFiles(ctx, conn, service, method)
	if err != nil {
		return nil, err
	}
	reflected.put(rkey, f)
	return f, nil
}
