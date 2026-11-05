package backup

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
)

// sprIdentity is what a node's SPR says that matters for reaching it: its
// peer ID and addresses, without the sequence number, which changes each
// time the node starts. A daemon restarted its own node whenever a peer's
// SPR string changed, and k11's changed on every restart of k11, so scribe's
// node restarted and lost the provider records it held (2026-11-05). An SPR
// that does not parse is its own identity.
func sprIdentity(spr string) string {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(spr, "spr:"))
	if err != nil {
		return spr
	}
	// Envelope: 1 public key, 2 payload type, 3 payload, 5 signature.
	payload, err := pbField(b, 3)
	if err != nil {
		return spr
	}
	// PeerRecord: 1 peer ID, 2 seq, 3 addresses (each: 1 multiaddr).
	id, err := pbField(payload, 1)
	if err != nil {
		return spr
	}
	var addrs []string
	for _, a := range pbFields(payload, 3) {
		if ma, err := pbField(a, 1); err == nil {
			addrs = append(addrs, string(ma))
		}
	}
	slices.Sort(addrs)
	return string(id) + "\x00" + strings.Join(addrs, "\x00")
}

func pbField(b []byte, num int) ([]byte, error) {
	if fs := pbFields(b, num); len(fs) > 0 {
		return fs[0], nil
	}
	return nil, errors.New("no such field")
}

// pbFields are the length-delimited fields numbered num in a protobuf
// message; it stops at the first thing it cannot read.
func pbFields(b []byte, num int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		key, n := uvarint(b)
		if n <= 0 {
			return out
		}
		b = b[n:]
		switch key & 7 {
		case 0: // varint
			_, n = uvarint(b)
			if n <= 0 {
				return out
			}
			b = b[n:]
		case 2: // length-delimited
			l, n := uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return out
			}
			if int(key>>3) == num {
				out = append(out, b[n:n+int(l)])
			}
			b = b[n+int(l):]
		default:
			return out
		}
	}
	return out
}

func uvarint(b []byte) (uint64, int) {
	var x uint64
	for i, c := range b {
		if i == 10 {
			return 0, -1
		}
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}
