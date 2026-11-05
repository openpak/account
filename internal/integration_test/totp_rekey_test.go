//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"openpak/account/internal/identity"
)

// A key change for the sealed two-factor secrets: a dry run changes nothing, the real run moves
// every row, the service on the new key still accepts the authenticator's codes, the old key no
// longer reads the rows, and a second run finds nothing to do.
func TestTOTPRekey(t *testing.T) {
	ident, _ := newEnv(t)
	ctx := context.Background()
	const pw = "correct horse battery"
	id, _ := registerAndLogin(t, ident, "rekey@example.com", pw)
	secret, _, err := ident.BeginTwoFactor(ctx, id, pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ident.EnableTwoFactor(ctx, id, totpAt(t, secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	pool := ident.Pool()
	read := func() []byte {
		var b []byte
		if err := pool.QueryRow(ctx, `SELECT secret_enc FROM account_two_factor WHERE account_id=$1`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := read()
	pepper := "test-pepper-0123456789abcdef0123456789abcdef" // the key rows are under when ACCOUNT_TOTP_KEY was never set
	newKey := strings.Repeat("n", 40)

	rep, err := identity.RekeyTOTP(ctx, pool, newKey, []string{strings.Repeat("z", 40), pepper}, false)
	if err != nil || rep.Total != 1 || rep.FromOld[1] != 1 || string(read()) != string(before) {
		t.Fatalf("dry run rep=%+v err=%v", rep, err)
	}
	if _, err := identity.RekeyTOTP(ctx, pool, newKey, []string{strings.Repeat("z", 40)}, true); err == nil {
		t.Fatal("a row under none of the given keys must fail the run")
	}
	if string(read()) != string(before) {
		t.Fatal("the failed run changed a row")
	}
	rep, err = identity.RekeyTOTP(ctx, pool, newKey, []string{pepper}, true)
	if err != nil || rep.Rekeyed() != 1 {
		t.Fatalf("apply rep=%+v err=%v", rep, err)
	}
	ident.SetTOTPKey(newKey)
	_, chal, _, err := ident.BeginLogin(ctx, "rekey@example.com", pw)
	if err != nil || chal == "" {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := ident.CompleteLogin(ctx, chal, totpAt(t, secret, time.Now().Add(30*time.Second)), ""); err != nil {
		t.Fatalf("a code from the authenticator must still work on the new key: %v", err)
	}
	rep, err = identity.RekeyTOTP(ctx, pool, newKey, []string{pepper}, true)
	if err != nil || rep.AlreadyNew != 1 || rep.Rekeyed() != 0 {
		t.Fatalf("second run rep=%+v err=%v", rep, err)
	}
}
