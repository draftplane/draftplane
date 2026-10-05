// Package identity mints and validates the short, human-legible identifiers
// that give each reviewing agent a distinct voice: a curated word pair plus
// two Crockford base32 characters, e.g. "blue-parakeet-f9".
//
// The suffix makes every identity unique by construction, so there is no
// issued-ids registry. Canonical can therefore tell a well-formed identity
// from a malformed one, never an issued one from an invented one: nothing
// here stops one agent from presenting another's identity.
package identity

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
)

// crockfordAlphabet is Crockford's base32 symbol set: 0-9 and a-z minus i,
// l, o and u. Canonical folds i/l and o back to 1 and 0; u has no fold.
const crockfordAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// identityPattern is the canonical shape: two lowercase words of at most
// eight characters -- the cap words.go's entries are curated to -- joined by
// a Crockford pair. Canonical validates identities this package did not
// mint, so the cap is enforced here too.
var identityPattern = regexp.MustCompile(`^[a-z]{1,8}-[a-z]{1,8}-[` + crockfordAlphabet + `]{2}$`)

// crockfordFold resolves the two visual confusions in Crockford's alphabet
// so a suffix retyped by eye still round-trips. u has no fold: it is
// excluded outright rather than aliased to anything.
var crockfordFold = strings.NewReplacer("i", "1", "l", "1", "o", "0")

// MintPair returns a fresh curated word pair with no suffix, e.g.
// "calm-mountain".
//
// A bare pair is client/config's machine pseudonym: the stable name for the
// human on this machine. Agent identities add the suffix because
// many agents exist at once; a machine has exactly one pseudonym.
func MintPair() string {
	return fmt.Sprintf("%s-%s", pick(adjectives), pick(nouns))
}

// Mint returns a fresh identity: two curated words and two Crockford base32
// characters, e.g. "blue-parakeet-f9".
func Mint() string {
	return fmt.Sprintf("%s-%s", MintPair(), suffix())
}

// Canonical validates shape and normalizes an identity a caller sent back.
// ok is false for anything malformed, which callers treat as absent.
//
// Validation is shape-only. What it does stop is a crafted identity carrying
// an extra segment -- "eve-agent-blue-thunder" -- from being read as someone
// else's two-segment identity by whatever renders it.
func Canonical(s string) (string, bool) {
	parts := strings.Split(strings.ToLower(s), "-")
	if len(parts) != 3 {
		return "", false
	}
	folded := parts[0] + "-" + parts[1] + "-" + crockfordFold.Replace(parts[2])
	if !identityPattern.MatchString(folded) {
		return "", false
	}
	return folded, true
}

func pick(words []string) string {
	return words[randIndex(len(words))]
}

func suffix() string {
	b := make([]byte, 2)
	for i := range b {
		b[i] = crockfordAlphabet[randIndex(len(crockfordAlphabet))]
	}
	return string(b)
}

// randIndex returns a uniform index in [0, n). crypto/rand's error is
// ignored, unreachable in practice; four bytes rather than one keeps the
// modulo bias immeasurable at this wordlist's size.
func randIndex(n int) int {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint32(b[:]) % uint32(n))
}
