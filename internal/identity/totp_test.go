package identity

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA-1 column, truncated to the 6 digits we use (the
// last six of the 8-digit reference values).
func TestTOTPReferenceVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		if got := hotp(secret, totpStep(time.Unix(v.unix, 0)), 6); got != v.want {
			t.Errorf("T=%d: got %s want %s", v.unix, got, v.want)
		}
	}
	// And the full 8-digit reference for one of them.
	if got := hotp(secret, totpStep(time.Unix(59, 0)), 8); got != "94287082" {
		t.Errorf("8 digits: %s", got)
	}
}

func TestTOTPWindow(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	for _, d := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code := hotp(secret, totpStep(now.Add(d)), 6)
		if step, ok := totpMatch(secret, code, now); !ok || step != totpStep(now.Add(d)) {
			t.Errorf("drift %v rejected", d)
		}
	}
	if _, ok := totpMatch(secret, hotp(secret, totpStep(now.Add(-90*time.Second)), 6), now); ok {
		t.Error("code three steps old accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := totpMatch(secret, bad, now); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSealerBindsAccount(t *testing.T) {
	s := newSealer("k")
	sealed, err := s.seal([]byte("secret"), "acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.open(sealed, "acct-a"); err != nil || string(got) != "secret" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := s.open(sealed, "acct-b"); err == nil {
		t.Fatal("ciphertext moved to another account decrypted")
	}
	if _, err := newSealer("other").open(sealed, "acct-a"); err == nil {
		t.Fatal("wrong key decrypted")
	}
}

func TestOtpauthURIAndRecoveryCodes(t *testing.T) {
	u := otpauthURI("JBSWY3DPEHPK3PXP", "me@example.com")
	if !strings.HasPrefix(u, "otpauth://totp/OpenPak:me@example.com?") || !strings.Contains(u, "secret=JBSWY3DPEHPK3PXP") ||
		!strings.Contains(u, "issuer=OpenPak") || !strings.Contains(u, "digits=6") || !strings.Contains(u, "period=30") {
		t.Fatalf("uri: %s", u)
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c, err := newRecoveryCode()
		if err != nil || len(c) != 11 || c[5] != '-' || seen[c] {
			t.Fatalf("code %q %v", c, err)
		}
		seen[c] = true
		if normalizeRecoveryCode(" "+strings.ToUpper(c)+" ") != strings.ReplaceAll(c, "-", "") {
			t.Fatalf("normalize %q", c)
		}
	}
}
