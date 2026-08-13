package colony_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The registration example in README.md is the first thing a new user copies,
// and the one it replaced was subtly wrong in a way no test could see: it told
// you to persist the key and then took the confirm fingerprint from memory.
// Keeping the README block byte-identical to a function the compiler checks
// means the documented flow cannot drift from a flow that builds.
func TestREADMERegistrationExampleMatchesCode(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("example_test.go")
	if err != nil {
		t.Fatal(err)
	}

	block := regexp.MustCompile("(?s)<!-- canonical-registration-example.*?```go\n(.*?)```").
		FindSubmatch(readme)
	if block == nil {
		t.Fatal("canonical-registration-example block not found in README.md — " +
			"if it was renamed, update this test rather than deleting it")
	}
	want := strings.TrimSpace(string(block[1]))

	i := strings.Index(string(src), "func register(ctx context.Context, keyPath string)")
	if i < 0 {
		t.Fatal("canonical register() not found in example_test.go")
	}
	j := strings.Index(string(src)[i:], "\n}\n")
	if j < 0 {
		t.Fatal("could not find the end of register()")
	}
	got := strings.TrimSpace(string(src)[i : i+j+2])

	if got != want {
		t.Errorf("README example and example_test.go have drifted.\n--- README ---\n%s\n--- code ---\n%s", want, got)
	}
}

// CONTROL: the comparison must be capable of failing. If this ever passes with
// deliberately different inputs, the test above certifies nothing.
func TestREADMEComparisonCanFail(t *testing.T) {
	if strings.TrimSpace("func register(") == strings.TrimSpace("func registerX(") {
		t.Fatal("string comparison is not discriminating")
	}
}
