package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

func (r *Resolver) resolveFunction(spec string) (string, error) {
	parts := strings.Split(spec, ":")
	name, args := parts[0], parts[1:]
	switch name {
	case "uuid":
		if err := noFunctionArgs(name, args); err != nil {
			return "", err
		}
		return r.uuid()
	case "timestamp":
		if err := noFunctionArgs(name, args); err != nil {
			return "", err
		}
		return strconv.FormatInt(r.now().Unix(), 10), nil
	case "timestampMs":
		if err := noFunctionArgs(name, args); err != nil {
			return "", err
		}
		return strconv.FormatInt(r.now().UnixMilli(), 10), nil
	case "isoTimestamp":
		if err := noFunctionArgs(name, args); err != nil {
			return "", err
		}
		return r.now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), nil
	case "randomInt":
		return r.randomInt(args)
	case "randomHex":
		return r.randomHex(args)
	default:
		return "", verr.Newf(verr.CodeConfig, "unknown interpolation function %q", name)
	}
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) random() io.Reader {
	if r.Rand != nil {
		return r.Rand
	}
	return rand.Reader
}

func (r *Resolver) uuid() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r.random(), b[:]); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "generate uuid interpolation value")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32], nil
}

func (r *Resolver) randomInt(args []string) (string, error) {
	min, max := int64(0), int64(1000)
	if len(args) != 0 {
		if len(args) != 2 {
			return "", verr.New(verr.CodeConfig, "randomInt expects either no arguments or MIN:MAX")
		}
		var err error
		min, err = strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return "", verr.Newf(verr.CodeConfig, "invalid randomInt MIN %q", args[0])
		}
		max, err = strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return "", verr.Newf(verr.CodeConfig, "invalid randomInt MAX %q", args[1])
		}
		if min > max {
			return "", verr.Newf(verr.CodeConfig, "randomInt MIN %d exceeds MAX %d", min, max)
		}
	}

	span := new(big.Int).Sub(big.NewInt(max), big.NewInt(min))
	span.Add(span, big.NewInt(1))
	n, err := rand.Int(r.random(), span)
	if err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "generate randomInt interpolation value")
	}
	return n.Add(n, big.NewInt(min)).String(), nil
}

func (r *Resolver) randomHex(args []string) (string, error) {
	if len(args) != 1 {
		return "", verr.New(verr.CodeConfig, "randomHex expects exactly one N argument")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		return "", verr.Newf(verr.CodeConfig, "invalid randomHex length %q; want a positive integer", args[0])
	}
	b := make([]byte, n/2+n%2)
	if _, err := io.ReadFull(r.random(), b); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "generate randomHex interpolation value")
	}
	return hex.EncodeToString(b)[:n], nil
}

func noFunctionArgs(name string, args []string) error {
	if len(args) != 0 {
		return verr.Newf(verr.CodeConfig, "interpolation function %q does not accept arguments", name)
	}
	return nil
}
