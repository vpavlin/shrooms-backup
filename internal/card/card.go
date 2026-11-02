// Package card derives Shrooms Backup's keys from a Keycard (ADR-001).
//
// One card, a key for every machine, each worked out again whenever it is
// needed: there is nothing to write down beyond the card's own recovery
// phrase. The card goes into whatever has a reader or NFC (the laptop, the
// phone), never into the machine being backed up; the key reaches that
// machine over the mesh.
//
// The derivation is BIP85 (the card's EXPORT BIP85): entropy from the card's
// seed at a path, while the seed never leaves the card. The path is BIP85's
// HEX application, 32 bytes, at an index fixed by the machine's name:
// m/83696968'/128169'/32'/INDEX', the index being the first 31 bits of
// SHA-256("shrooms-backup " + name). No table anywhere, and a machine
// reinstalled under its name gets its key back.
package card

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	kc "github.com/keycard-tech/keycard-go/v4"
	kio "github.com/keycard-tech/keycard-go/v4/io"
	ktypes "github.com/keycard-tech/keycard-go/v4/types"
)

// Transport moves one APDU to the card: a reader here, IsoDep on a phone.
type Transport interface {
	Transmit(apdu []byte) ([]byte, error)
}

// Path is where a machine's backup key is derived. Changing it would leave
// every machine's backups unopenable from the card (test pins it).
func Path(machine string) string {
	h := sha256.Sum256([]byte("shrooms-backup " + strings.TrimSpace(machine)))
	return fmt.Sprintf("m/83696968'/128169'/32'/%d'", binary.BigEndian.Uint32(h[:4])&0x7fffffff)
}

// Fingerprint names a key without showing it.
func Fingerprint(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:4])
}

// Key is a machine's backup key, 64 hex characters. pair is asked for the
// card's pairing password the first time this device meets the card (an
// applet from 4.0 on needs none, and is paired with ""); the pairing is kept
// in dir afterwards. Shrooms Backup pairs on its own, in a slot of its own:
// it reads nothing of Shrooms'.
func Key(t Transport, dir, pin, machine string, pair func() (string, error)) (string, error) {
	if strings.TrimSpace(machine) == "" {
		return "", errors.New("which machine?")
	}
	cs := kc.NewCommandSet(kio.NewNormalChannel(t))
	if err := cs.Select(); err != nil {
		return "", fmt.Errorf("no Keycard applet on this card: %w", err)
	}
	file := filepath.Join(dir, "keycard-pairing")
	if raw, err := os.ReadFile(file); err == nil {
		p, err := decodePairing(strings.TrimSpace(string(raw)))
		if err != nil {
			return "", err
		}
		cs.SetPairing(p)
		if err := cs.AutoOpenSecureChannel(); err != nil {
			return "", fmt.Errorf("could not open a secure channel: a different card, or the pairing "+
				"in %s is stale (initialising a card clears its pairings); delete it to pair again: %w", file, err)
		}
	} else {
		pw := ""
		if cs.ApplicationInfo != nil && needsPassword(cs.ApplicationInfo) {
			if pw, err = pair(); err != nil {
				return "", err
			}
		}
		if err := cs.AutoPairWithMode(pw, kc.P2PairAny); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "6a84") {
				return "", errors.New("the card's five pairing slots are full; free one from a device paired with it")
			}
			return "", fmt.Errorf("pairing with the card: %w", err)
		}
		if err := cs.AutoOpenSecureChannel(); err != nil {
			return "", fmt.Errorf("paired, but could not open a secure channel: %w", err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(file, []byte(encodePairing(cs.Pairing())+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("paired, but could not keep the pairing: %w", err)
		}
	}
	if err := cs.VerifyPIN(pin); err != nil {
		return "", fmt.Errorf("the PIN was refused (the card locks after three): %w", err)
	}
	b, err := cs.ExportBIP85(Path(machine), 32)
	if err != nil {
		return "", fmt.Errorf("the card would not derive a backup key (EXPORT BIP85; its applet may predate BIP85): %w", err)
	}
	if len(b) != 32 {
		return "", fmt.Errorf("the card returned %d bytes for a 32-byte key", len(b))
	}
	return hex.EncodeToString(b), nil
}

// needsPassword: applets before 4.0 pair with a password; from 4.0 on with
// a certificate.
func needsPassword(a *ktypes.ApplicationInfo) bool {
	return len(a.Version) > 0 && a.Version[0] < 4
}

func encodePairing(p *ktypes.Pairing) string {
	key := p.Key()
	return base64.StdEncoding.EncodeToString(append([]byte{p.Index()}, key[:]...))
}

func decodePairing(s string) (*ktypes.Pairing, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 33 {
		return nil, errors.New("the kept pairing is corrupt; delete it to pair again")
	}
	var key [32]byte
	copy(key[:], b[1:])
	return ktypes.NewPairing(key, b[0]), nil
}
