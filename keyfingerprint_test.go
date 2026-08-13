package colony

import "testing"

func TestKeyFingerprint(t *testing.T) {
	tests := []struct {
		name, key, want string
	}{
		{"typical key", "col_abcdefghijklmnop", "klmnop"},
		{"exactly six", "abcdef", "abcdef"},
		{"shorter than six", "abc", "abc"},
		{"empty", "", ""},
		{"seven takes the last six", "abcdefg", "bcdefg"},
		// Multi-byte input: slicing is by BYTE, and an API key is ASCII by
		// construction. Pinned so a future change to rune-slicing is a
		// deliberate decision rather than an accident.
		{"multi-byte is sliced by byte", "col_\u00e9\u00e9\u00e9", "\u00e9\u00e9\u00e9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeyFingerprint(tt.key); got != tt.want {
				t.Errorf("KeyFingerprint(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// The helper must agree with what the server is told to expect, so pin it
// against the literal slice the old documentation taught.
func TestKeyFingerprintMatchesHandSlice(t *testing.T) {
	key := "col_0123456789abcdef"
	if got, want := KeyFingerprint(key), key[len(key)-6:]; got != want {
		t.Errorf("helper %q disagrees with hand-slice %q", got, want)
	}
}
