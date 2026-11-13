package exitcode

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
)

// TestCodeValuesFrozen pins the numeric table. Changing a value is a breaking
// contract change and must change this test on purpose.
func TestCodeValuesFrozen(t *testing.T) {
	want := map[string]struct{ got, want Code }{
		"green": {Green, 0}, "yellow": {Yellow, 1}, "red": {Red, 2},
		"config": {Config, 3}, "auth": {Auth, 4}, "tls": {TLS, 5},
		"transport": {Transport, 6}, "internal": {Internal, 7}, "usage": {Usage, 8},
	}
	for name, c := range want {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", name, c.got, c.want)
		}
	}
}

func TestReasonsUniqueSnakeCaseAndCoded(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
	seen := map[Reason]bool{}
	for _, r := range All() {
		if !snake.MatchString(string(r)) {
			t.Errorf("reason %q is not snake_case", r)
		}
		if seen[r] {
			t.Errorf("duplicate reason %q", r)
		}
		seen[r] = true
	}
	for r, c := range codes {
		if !seen[r] {
			t.Errorf("coded reason %q missing from All()", r)
		}
		if c < Config || c > Internal {
			t.Errorf("reason %q carries %d; only 3-7 are error codes", r, c)
		}
	}
}

func TestAllowlistRefusalIsInternalSeven(t *testing.T) {
	e := New(AllowlistDenied, "scry is inspect-only; refused locally before network: DELETE /api/v1/nodes")
	if int(e.Code) != 7 {
		t.Fatalf("allowlist refusal carries %d, want 7", e.Code)
	}
}

func TestErrorsIsMatchesByReason(t *testing.T) {
	err := fmt.Errorf("load: %w", New(ExecAuthRefused, "user %q uses exec", "u"))
	if !errors.Is(err, ErrExecAuth) {
		t.Fatal("errors.Is(ErrExecAuth) = false")
	}
	if errors.Is(err, ErrAuthProvider) {
		t.Fatal("exec refusal must not match auth-provider")
	}
	if CodeOf(err) != Config {
		t.Fatalf("CodeOf = %d, want %d", CodeOf(err), Config)
	}
}

func TestCodeOfUntypedIsInternal(t *testing.T) {
	if CodeOf(errors.New("boom")) != Internal {
		t.Fatal("untyped error must map to internal")
	}
	if CodeOf(nil) != Green {
		t.Fatal("nil must map to 0")
	}
}

func TestDecorativeReasonsCarryNoCode(t *testing.T) {
	for _, r := range []Reason{ExpectedDark, ApiserverDown, ReadyzFailed, NoNodes, NodeNotReady, NetworkUnavailable, Timeout} {
		if _, ok := ForReason(r); ok {
			t.Errorf("%q must not carry an error code", r)
		}
	}
}
