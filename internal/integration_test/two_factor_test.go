//go:build integration

package integration_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"openpak/account/internal/identity"
	"openpak/account/internal/web"
	accountv1 "openpak/account/proto/openpak/account/v1"
)

// totpAt is an independent RFC 6238 implementation (SHA-1, 6 digits, 30 s),
// so the test does not grade the core with its own code.
func totpAt(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[19] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:])&0x7fffffff)%1000000)
}

func sessionCookie(rec interface{ Result() *http.Response }) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "openpak_session" && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func TestTwoFactor(t *testing.T) {
	idc, _, _, _, _, _, ident := startGRPC(t)
	ctx := context.Background()
	gctx := metadata.AppendToOutgoingContext(ctx, "x-internal-key", grpcInternalKey)
	box := &mailbox{}
	h := web.New(ident, box.mailer(), "https://accounts.test", "")
	id, sess := registerAndLogin(t, ident, "2fa@example.com", "correct horse battery")
	resetFailures := func() {
		if _, err := ident.Pool().Exec(ctx, `UPDATE account_two_factor SET failures=0, failures_since=NULL`); err != nil {
			t.Fatal(err)
		}
	}
	login := func() string {
		t.Helper()
		rec, body := call(t, h, "POST", "/v1/login", "", `{"email":"2fa@example.com","password":"correct horse battery"}`)
		if rec.Code != http.StatusAccepted || body["two_factor_required"] != true || sessionCookie(rec) != "" {
			t.Fatalf("login with 2fa: %d %v", rec.Code, body)
		}
		return body["challenge"].(string)
	}

	// Enrolment.
	if rec, _ := call(t, h, "POST", "/v1/2fa/setup", sess, `{"password":"wrong password 1"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("setup with wrong password: %d", rec.Code)
	}
	rec, body := call(t, h, "POST", "/v1/2fa/setup", sess, `{"password":"correct horse battery"}`)
	secret, _ := body["secret"].(string)
	uri, _ := body["otpauth_uri"].(string)
	if rec.Code != 200 || len(secret) != 32 || !strings.HasPrefix(uri, "otpauth://totp/OpenPak:2fa@example.com?") || !strings.Contains(uri, secret) {
		t.Fatalf("setup: %d %v", rec.Code, body)
	}
	var stored []byte
	if err := ident.Pool().QueryRow(ctx, `SELECT secret_enc FROM account_two_factor WHERE account_id=$1`, id).Scan(&stored); err != nil ||
		strings.Contains(string(stored), secret) {
		t.Fatalf("secret must be sealed at rest: %v", err)
	}
	// Not enabled yet: sign-in is unchanged.
	if _, _, err := ident.Login(ctx, "2fa@example.com", "correct horse battery"); err != nil {
		t.Fatalf("pending enrolment must not gate login: %v", err)
	}
	if rec, _ := call(t, h, "POST", "/v1/2fa/enable", sess, `{"code":"000000"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("enable with wrong code: %d", rec.Code)
	}
	now := time.Now()
	rec, body = call(t, h, "POST", "/v1/2fa/enable", sess, `{"code":"`+totpAt(t, secret, now)+`"}`)
	rawCodes, _ := body["recovery_codes"].([]any)
	if rec.Code != 200 || len(rawCodes) != 10 {
		t.Fatalf("enable: %d %v", rec.Code, body)
	}
	recovery := make([]string, len(rawCodes))
	for i, c := range rawCodes {
		recovery[i] = c.(string)
	}
	if rec, _ := call(t, h, "POST", "/v1/2fa/setup", sess, `{"password":"correct horse battery"}`); rec.Code != http.StatusConflict {
		t.Fatalf("setup while enabled: %d", rec.Code)
	}
	if _, _, err := ident.Login(ctx, "2fa@example.com", "correct horse battery"); err != identity.ErrTwoFactorRequired {
		t.Fatalf("Login must not mint a session past 2FA: %v", err)
	}

	// Sign-in: the code used to enable cannot be replayed; the next one works.
	ch := login()
	if rec, _ := call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+ch+`","code":"`+totpAt(t, secret, now)+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code: %d", rec.Code)
	}
	rec, body = call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+ch+`","code":"`+totpAt(t, secret, now.Add(30*time.Second))+`"}`)
	if rec.Code != 200 || sessionCookie(rec) == "" {
		t.Fatalf("login/2fa: %d %v", rec.Code, body)
	}
	if acct, err := ident.SessionAccount(ctx, sessionCookie(rec)); err != nil || acct.ID != id {
		t.Fatalf("2fa session: %v", err)
	}
	if rec, _ := call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+ch+`","recovery_code":"`+recovery[0]+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("challenge is single-use: %d", rec.Code)
	}

	// Recovery codes: once each, typed however.
	rec, _ = call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+login()+`","recovery_code":" `+strings.ToUpper(recovery[0])+` "}`)
	if rec.Code != 200 || sessionCookie(rec) == "" {
		t.Fatalf("recovery code: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+login()+`","recovery_code":"`+recovery[0]+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("recovery code reuse: %d", rec.Code)
	}

	// Rate limit: five wrong codes lock the second factor for every path.
	resetFailures()
	ch = login()
	for i := 0; i < 5; i++ {
		if rec, _ := call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+ch+`","code":"000000"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: %d", i, rec.Code)
		}
	}
	good := totpAt(t, secret, time.Now().Add(30*time.Second))
	if _, err := ident.Pool().Exec(ctx, `UPDATE account_two_factor SET last_step=0`); err != nil {
		t.Fatal(err)
	}
	if rec, _ := call(t, h, "POST", "/v1/login/2fa", "", `{"challenge":"`+login()+`","code":"`+good+`"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked: %d", rec.Code)
	}
	if _, err := idc.VerifySecondFactor(gctx, &accountv1.VerifySecondFactorRequest{AccountId: id, Code: good}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("lock applies over gRPC too: %v", err)
	}
	resetFailures()

	// gRPC: a password alone never passes for this account.
	if _, err := idc.VerifyPassword(gctx, &accountv1.VerifyPasswordRequest{CallerNamespace: "switch", Purpose: "console_login",
		Email: "2fa@example.com", Password: "correct horse battery"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy caller must fail closed: %v", err)
	}
	if _, err := idc.VerifyPasswordByID(gctx, &accountv1.VerifyPasswordByIDRequest{CallerNamespace: "wiiu", Purpose: "console_login",
		AccountId: id, Password: "correct horse battery"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy caller by id must fail closed: %v", err)
	}
	if _, err := idc.VerifyPassword(gctx, &accountv1.VerifyPasswordRequest{CallerNamespace: "website", Purpose: "x",
		Email: "2fa@example.com", Password: "wrong password 1", SecondFactorSupported: false}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong password is checked first: %v", err)
	}
	vp, err := idc.VerifyPassword(gctx, &accountv1.VerifyPasswordRequest{CallerNamespace: "website", Purpose: "x",
		Email: "2fa@example.com", Password: "correct horse battery", SecondFactorSupported: true})
	if err != nil || !vp.GetTwoFactorRequired() || vp.GetAccountId() != id {
		t.Fatalf("aware caller: %+v %v", vp, err)
	}
	vb, err := idc.VerifyPasswordByID(gctx, &accountv1.VerifyPasswordByIDRequest{CallerNamespace: "website", Purpose: "x",
		AccountId: id, Password: "correct horse battery", SecondFactorSupported: true})
	if err != nil || !vb.GetTwoFactorRequired() {
		t.Fatalf("aware caller by id: %+v %v", vb, err)
	}
	if _, err := idc.VerifySecondFactor(gctx, &accountv1.VerifySecondFactorRequest{AccountId: id, Code: "123456"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong second factor: %v", err)
	}
	sf, err := idc.VerifySecondFactor(gctx, &accountv1.VerifySecondFactorRequest{AccountId: id, RecoveryCode: recovery[1]})
	if err != nil || !sf.GetRecoveryCodeUsed() || sf.GetRecoveryCodesLeft() != 8 {
		t.Fatalf("second factor by recovery code: %+v %v", sf, err)
	}
	if ga, err := idc.GetAccount(gctx, &accountv1.GetAccountRequest{AccountId: id}); err != nil || !ga.GetTwoFactorEnabled() {
		t.Fatalf("GetAccount two_factor_enabled: %+v %v", ga, err)
	}

	// Disable: password and a code.
	if rec, _ := call(t, h, "POST", "/v1/2fa/disable", sess, `{"password":"wrong password 1","code":"`+recovery[2]+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("disable with wrong password: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/2fa/disable", sess, `{"password":"correct horse battery","code":"000000"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable with wrong code: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/2fa/disable", sess, `{"password":"correct horse battery","code":"`+recovery[2]+`"}`); rec.Code != 200 {
		t.Fatalf("disable: %d", rec.Code)
	}
	if rec, _ := call(t, h, "POST", "/v1/2fa/disable", sess, `{"password":"correct horse battery","code":"`+recovery[3]+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("disable twice: %d", rec.Code)
	}
	rec, _ = call(t, h, "POST", "/v1/login", "", `{"email":"2fa@example.com","password":"correct horse battery"}`)
	if rec.Code != 200 || sessionCookie(rec) == "" {
		t.Fatalf("login after disable: %d", rec.Code)
	}
	if vp, err := idc.VerifyPassword(gctx, &accountv1.VerifyPasswordRequest{CallerNamespace: "switch", Purpose: "console_login",
		Email: "2fa@example.com", Password: "correct horse battery"}); err != nil || vp.GetTwoFactorRequired() {
		t.Fatalf("legacy caller after disable: %+v %v", vp, err)
	}
}

// A changed ACCOUNT_TOTP_KEY makes TOTP impossible, but recovery codes still
// sign the owner in.
func TestTwoFactorKeyChangeLeavesRecovery(t *testing.T) {
	_, _, _, _, _, _, ident := startGRPC(t)
	ctx := context.Background()
	id, _ := registerAndLogin(t, ident, "rekey@example.com", "correct horse battery")
	secret, _, err := ident.BeginTwoFactor(ctx, id, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	codes, err := ident.EnableTwoFactor(ctx, id, totpAt(t, secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	ident.SetTOTPKey("a-completely-different-key-0123456789abcdef")
	if _, err := ident.VerifySecondFactor(ctx, id, totpAt(t, secret, time.Now().Add(30*time.Second)), ""); err == nil ||
		err == identity.ErrBadSecondFactor {
		t.Fatalf("TOTP with the wrong key must be an internal error, not a counted guess: %v", err)
	}
	if res, err := ident.VerifySecondFactor(ctx, id, "", codes[0]); err != nil || !res.RecoveryCodeUsed {
		t.Fatalf("recovery code after key change: %+v %v", res, err)
	}
}
