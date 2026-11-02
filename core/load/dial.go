package load

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

// remoteRefusalPrefix is the dial-guard error. A step error that contains it
// stops the load run (exit 3): the address was not the one the pre-run
// screen saw (a redirect, a proxy, a feed row, or a captured host).
const remoteRefusalPrefix = "refusing non-private address "

func remoteRefusal(addr string) error {
	return fmt.Errorf("%s%s; pass --allow-remote", remoteRefusalPrefix, addr)
}

// refusalText returns the dial-guard sentence inside a step error, or "".
func refusalText(msg string) string {
	i := strings.Index(msg, remoteRefusalPrefix)
	if i < 0 {
		return ""
	}
	return msg[i:]
}

// guardedDial is the dialer for a load run that has not passed --allow-remote.
// It resolves host (unless it is already an IP), rejects any address outside
// the private allow-list, and dials an address it has already checked so a
// DNS change between the lookup and the connect cannot slip through.
// lookup nil uses the system resolver. A proxy dial goes through the same
// function: the transport dials the proxy, and the guard sees that IP.
func guardedDial(lookup func(context.Context, string) ([]net.IP, error)) func(context.Context, string, string) (net.Conn, error) {
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolveDialHost(ctx, host, lookup)
		if err != nil || len(ips) == 0 {
			return nil, remoteRefusal(host)
		}
		for _, ip := range ips {
			if !allowedIP(ip) {
				return nil, remoteRefusal(ip.String())
			}
		}
		var chosen net.IP
		for _, ip := range ips {
			if dialFamily(network, ip) {
				chosen = ip
				break
			}
		}
		if chosen == nil {
			return nil, remoteRefusal(host)
		}
		d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
	}
}

func resolveDialHost(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IP, error)) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	return lookup(ctx, host)
}

func dialFamily(network string, ip net.IP) bool {
	switch network {
	case "tcp4", "udp4":
		return ip.To4() != nil
	case "tcp6", "udp6":
		return ip.To4() == nil
	default:
		return true
	}
}

// configRefusal wraps a dial-guard sentence as the config error the CLI
// reports with exit 3.
func configRefusal(msg string) error {
	return verr.New(verr.CodeConfig, msg)
}
