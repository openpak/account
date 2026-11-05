//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"openpak/account/internal/identity"
)

// The password pepper moves over without anyone being locked out: hashes made under the old
// pepper keep working, are re-hashed under the new one at the next successful check, wrong
// passwords fail under both, adapter credentials follow, and sessions made before the change
// still resolve.
func TestPepperCutover(t *testing.T) {
	ident, _ := newEnv(t) // built with the old pepper
	ctx := context.Background()
	const pw = "correct horse battery"
	id, sess := registerAndLogin(t, ident, "pepper@example.com", pw)
	if err := ident.SetAdapterCredential(ctx, "wiiu", id, "console-hash"); err != nil {
		t.Fatal(err)
	}
	gen := func(q string) int {
		var g int
		if err := ident.Pool().QueryRow(ctx, q, id).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	hash := func() string {
		var h string
		if err := ident.Pool().QueryRow(ctx, `SELECT password_hash FROM accounts WHERE id=$1`, id).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	const accGen = `SELECT pepper_gen FROM accounts WHERE id=$1`
	const adGen = `SELECT pepper_gen FROM adapter_credentials WHERE account_id=$1`
	if gen(accGen) != 0 || gen(adGen) != 0 {
		t.Fatal("rows made before the cutover must be generation 0")
	}

	// the same database, now with the new pepper current and the old one kept to verify
	next := identity.New(ident.Pool(), "test-pepper-0123456789abcdef0123456789abcdef", time.Hour, nil)
	next.SetNewPepper(strings.Repeat("N", 40))
	before := hash()

	if _, err := next.VerifyCredentials(ctx, "pepper@example.com", "wrong password 1"); err != identity.ErrBadCredentials {
		t.Fatalf("wrong password on an old hash: %v", err)
	}
	if hash() != before || gen(accGen) != 0 {
		t.Fatal("a failed check changed the stored hash")
	}
	if _, err := next.VerifyByID(ctx, id, pw); err != nil { // the adapter path
		t.Fatalf("old hash must still verify: %v", err)
	}
	if hash() == before || gen(accGen) != 1 {
		t.Fatal("a successful check did not move the hash to the new pepper")
	}
	// now on the new pepper: works, and wrong still fails
	if _, err := next.VerifyCredentials(ctx, "pepper@example.com", pw); err != nil {
		t.Fatalf("new hash: %v", err)
	}
	if _, err := next.VerifyByID(ctx, id, "wrong password 2"); err != identity.ErrBadCredentials {
		t.Fatalf("wrong password on a new hash: %v", err)
	}
	// the service with only the old pepper can no longer verify it: the move really happened
	if _, err := ident.VerifyCredentials(ctx, "pepper@example.com", pw); err == nil {
		t.Fatal("the old pepper still verifies a re-hashed password")
	}

	// adapter credentials
	if _, err := next.VerifyAdapterCredential(ctx, "wiiu", id, "wrong"); err != identity.ErrBadCredentials {
		t.Fatalf("wrong adapter secret: %v", err)
	}
	if _, err := next.VerifyAdapterCredential(ctx, "wiiu", id, "console-hash"); err != nil {
		t.Fatalf("old adapter credential: %v", err)
	}
	if gen(adGen) != 1 {
		t.Fatal("the adapter credential was not moved")
	}
	if _, err := next.VerifyAdapterCredential(ctx, "wiiu", id, "console-hash"); err != nil {
		t.Fatalf("new adapter credential: %v", err)
	}

	// a session made under the old pepper still resolves; a new account is born on the new pepper
	if a, err := next.SessionAccount(ctx, sess); err != nil || a.ID != id {
		t.Fatalf("old session: %v", err)
	}
	acct, _, err := next.Register(ctx, "fresh@example.com", pw, "Fresh")
	if err != nil {
		t.Fatal(err)
	}
	var g int
	if err := next.Pool().QueryRow(ctx, `SELECT pepper_gen FROM accounts WHERE id=$1`, acct.ID).Scan(&g); err != nil || g != 1 {
		t.Fatalf("a new account must be generation 1: %d %v", g, err)
	}
	if st, err := next.PepperStats(ctx); err != nil || st.Accounts[1] != 2 || st.Accounts[0] != 0 || st.AdapterCredentials[1] != 1 {
		t.Fatalf("stats %+v %v", st, err)
	}
	// changing the password under the new pepper
	if err := next.ChangePassword(ctx, id, pw, "another good password", sess); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, err := next.VerifyCredentials(ctx, "pepper@example.com", "another good password"); err != nil {
		t.Fatal(err)
	}
}
