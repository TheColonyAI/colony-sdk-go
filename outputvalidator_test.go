package colony

import (
	"strings"
	"testing"
)

// The length guard is what stops a long substantive post being judged by
// patterns meant for short error strings. Both sides of it, so the guard is
// shown to have a threshold rather than merely to exist.
func TestModelErrorLengthGuard(t *testing.T) {
	short := "An error occurred while processing."
	if !LooksLikeModelError(short) {
		t.Errorf("short error string not flagged: %q", short)
	}
	long := "An error occurred is a phrase I see too often, and here is why. " +
		strings.Repeat("x", modelErrorMaxLength)
	if LooksLikeModelError(long) {
		t.Error("a long post opening with a matching phrase was flagged — " +
			"a false positive here drops real content")
	}
}

// The guard counts runes. A byte count would put a 300-character CJK post
// over a 500-BYTE threshold and expose it to patterns it was never meant to
// face — the guard becoming the thing it exists to prevent.
func TestLengthGuardCountsRunesNotBytes(t *testing.T) {
	// 400 runes, 1200 bytes: under the rune limit, over any byte limit.
	body := "An error occurred. " + strings.Repeat("字", 400)
	if len(body) <= modelErrorMaxLength {
		t.Fatal("fixture is not multi-byte; the test would pass vacuously")
	}
	if len([]rune(body)) > modelErrorMaxLength {
		t.Fatal("fixture exceeds the rune limit; it tests the wrong branch")
	}
	if !LooksLikeModelError(body) {
		t.Error("a 400-rune string was treated as long — the guard counted bytes")
	}
}

func TestLooksLikeModelErrorIgnoresEmpty(t *testing.T) {
	for _, s := range []string{"", "   ", "\n\t"} {
		if LooksLikeModelError(s) {
			t.Errorf("empty input %q flagged as a model error", s)
		}
	}
}

func TestStripLLMArtifacts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<s>Assistant: Sure, here's the post: Hello!</s>", "Hello!"},
		{"[INST]prompt[/INST]answer", "promptanswer"},
		{"<|im_start|>assistant<|im_end|>text", "assistanttext"},
		{"Response:   the content", "the content"},
		{"  plain content  ", "plain content"},
		{"<s></s>", ""},
	}
	for _, c := range cases {
		if got := StripLLMArtifacts(c.in); got != c.want {
			t.Errorf("StripLLMArtifacts(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Only one preamble is stripped per output. Stacking strips starts eating
// real content, so this is a limit rather than an omission.
func TestOnlyOnePreambleIsStripped(t *testing.T) {
	got := StripLLMArtifacts("Sure, here's the post: Response: actual content")
	if got != "Response: actual content" {
		t.Errorf("got %q — a second preamble strip ran", got)
	}
}

// replaceFirst must replace the first match only. All current patterns are
// start-anchored so at most one exists, but relying on that silently is how
// an unanchored pattern added later starts rewriting the middle of a post.
func TestReplaceFirstReplacesOnlyTheFirstMatch(t *testing.T) {
	if got := replaceFirst(rolePrefix, "Assistant: talking about Assistant: prefixes", ""); got !=
		"talking about Assistant: prefixes" {
		t.Errorf("got %q", got)
	}
	if got := replaceFirst(rolePrefix, "no prefix here", ""); got != "no prefix here" {
		t.Errorf("a non-match was modified: %q", got)
	}
}

func TestValidateGeneratedOutputSurface(t *testing.T) {
	ok := ValidateGeneratedOutput("Assistant: substantive reply")
	if !ok.OK || ok.Content != "substantive reply" || ok.Reason != "" {
		t.Errorf("ok case = %+v", ok)
	}
	empty := ValidateGeneratedOutput("<s></s>")
	if empty.OK || empty.Reason != VerdictEmpty || empty.Content != "" {
		t.Errorf("empty case = %+v", empty)
	}
	bad := ValidateGeneratedOutput("Error generating text.")
	if bad.OK || bad.Reason != VerdictModelError {
		t.Errorf("error case = %+v", bad)
	}
	// A rejected result must carry no content — a caller that ignores OK and
	// reads Content should get nothing publishable, not the offending string.
	if bad.Content != "" {
		t.Errorf("a rejected result carried content: %q", bad.Content)
	}
}

// The order in ValidateGeneratedOutput is load-bearing: strip first, so a
// role-prefixed error string is classified as an error rather than slipping
// past the start-anchored patterns.
func TestStrippingHappensBeforeTheErrorCheck(t *testing.T) {
	got := ValidateGeneratedOutput("Assistant: Error generating text")
	if got.OK {
		t.Fatal("a role-prefixed error string was published")
	}
	if got.Reason != VerdictModelError {
		t.Errorf("Reason = %q, want %q — the prefix hid the pattern",
			got.Reason, VerdictModelError)
	}
}
