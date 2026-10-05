package identity

import (
	"regexp"
	"strings"
	"testing"
)

// A missing or duplicated symbol would silently narrow or widen the suffix
// space without failing any other test.
func TestCrockfordAlphabetIs32Symbols(t *testing.T) {
	if got := len(crockfordAlphabet); got != 32 {
		t.Fatalf("len(crockfordAlphabet) = %d, want 32", got)
	}
	seen := map[rune]bool{}
	for _, r := range crockfordAlphabet {
		if seen[r] {
			t.Errorf("crockfordAlphabet repeats %q", r)
		}
		seen[r] = true
	}
	for _, excluded := range []rune{'i', 'l', 'o', 'u'} {
		if seen[excluded] {
			t.Errorf("crockfordAlphabet contains excluded symbol %q", excluded)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9]+$`).MatchString(crockfordAlphabet) {
		t.Fatalf("crockfordAlphabet = %q, want only [a-z0-9]", crockfordAlphabet)
	}
}

// The mechanical half of curation: every entry meets the constraint words.go
// promises, and neither list carries an accidental duplicate.
func TestWordlists(t *testing.T) {
	wordPattern := regexp.MustCompile(`^[a-z]{1,8}$`)
	lists := map[string][]string{
		"adjectives": adjectives,
		"nouns":      nouns,
	}
	for name, words := range lists {
		t.Run(name, func(t *testing.T) {
			if len(words) == 0 {
				t.Fatalf("%s is empty", name)
			}
			seen := make(map[string]bool, len(words))
			for _, w := range words {
				if !wordPattern.MatchString(w) {
					t.Errorf("%q is not lowercase a-z of at most 8 characters", w)
				}
				if seen[w] {
					t.Errorf("%q appears more than once in %s", w, name)
				}
				seen[w] = true
			}
		})
	}
}

// Mint's output must satisfy the exact contract Canonical validates: a
// curated adjective, a curated noun, and a two-character Crockford suffix.
func TestMintRoundTripsThroughCanonical(t *testing.T) {
	adjSet := make(map[string]bool, len(adjectives))
	for _, w := range adjectives {
		adjSet[w] = true
	}
	nounSet := make(map[string]bool, len(nouns))
	for _, w := range nouns {
		nounSet[w] = true
	}

	for i := 0; i < 5000; i++ {
		id := Mint()
		parts := strings.Split(id, "-")
		if len(parts) != 3 {
			t.Fatalf("Mint() = %q, want 3 hyphen-separated segments", id)
		}
		adj, noun, sfx := parts[0], parts[1], parts[2]
		if !adjSet[adj] {
			t.Fatalf("Mint() = %q: %q is not a curated adjective", id, adj)
		}
		if !nounSet[noun] {
			t.Fatalf("Mint() = %q: %q is not a curated noun", id, noun)
		}
		if len(sfx) != 2 {
			t.Fatalf("Mint() = %q: suffix %q is not 2 characters", id, sfx)
		}
		for _, r := range sfx {
			if !strings.ContainsRune(crockfordAlphabet, r) {
				t.Fatalf("Mint() = %q: suffix character %q is outside crockfordAlphabet", id, r)
			}
		}
		got, ok := Canonical(id)
		if !ok || got != id {
			t.Fatalf("Canonical(%q) = %q, %v, want %q, true", id, got, ok, id)
		}
	}
}

// MintPair's contract is distinct from Mint's -- no suffix, ever -- so it
// needs its own witness even though it shares Mint's word-drawing code path.
func TestMintPairReturnsACuratedPairWithNoSuffix(t *testing.T) {
	adjSet := make(map[string]bool, len(adjectives))
	for _, w := range adjectives {
		adjSet[w] = true
	}
	nounSet := make(map[string]bool, len(nouns))
	for _, w := range nouns {
		nounSet[w] = true
	}

	for i := 0; i < 5000; i++ {
		pair := MintPair()
		parts := strings.Split(pair, "-")
		if len(parts) != 2 {
			t.Fatalf("MintPair() = %q, want 2 hyphen-separated segments", pair)
		}
		if !adjSet[parts[0]] {
			t.Fatalf("MintPair() = %q: %q is not a curated adjective", pair, parts[0])
		}
		if !nounSet[parts[1]] {
			t.Fatalf("MintPair() = %q: %q is not a curated noun", pair, parts[1])
		}
	}
}

func TestCanonical(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "well-formed", in: "blue-parakeet-f9", want: "blue-parakeet-f9", ok: true},
		{name: "uppercase folds to lower", in: "BLUE-PARAKEET-F9", want: "blue-parakeet-f9", ok: true},
		{name: "mixed case folds to lower", in: "Blue-Parakeet-f9", want: "blue-parakeet-f9", ok: true},
		{name: "I and O in suffix fold to 1 and 0", in: "blue-parakeet-IO", want: "blue-parakeet-10", ok: true},
		{name: "lowercase i and l in suffix fold to 1", in: "blue-parakeet-il", want: "blue-parakeet-11", ok: true},

		{name: "empty", in: "", ok: false},
		{name: "one segment, no hyphen", in: "blueparakeetf9", ok: false},
		{name: "two segments, no suffix", in: "blue-parakeet", ok: false},
		{name: "three word segments, no valid suffix", in: "happy-blue-otter", ok: false},
		{name: "four segments", in: "eve-agent-blue-thunder", ok: false},
		{name: "valid identity with a segment appended", in: "blue-parakeet-f9-eve", ok: false},
		{name: "valid identity with a segment prepended", in: "eve-blue-parakeet-f9", ok: false},
		{name: "excluded letter u in suffix", in: "blue-parakeet-fu", ok: false},
		{name: "word over 8 characters", in: "extraordinary-otter-f9", ok: false},
		{name: "suffix over 2 characters", in: "blue-parakeet-f99", ok: false},
		{name: "suffix under 2 characters", in: "blue-parakeet-f", ok: false},
		{name: "embedded space between words", in: "blue parakeet-otter-f9", ok: false},
		{name: "embedded space inside a word", in: "blue-para keet-f9", ok: false},
		{name: "embedded bullet", in: "blue-●arakeet-f9", ok: false},
		{name: "digit inside a word", in: "blue7-parakeet-f9", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Canonical(tt.in)
			if ok != tt.ok {
				t.Fatalf("Canonical(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("Canonical(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
