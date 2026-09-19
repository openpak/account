package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP per RFC 6238 with the parameters every authenticator app defaults to:
// HMAC-SHA1, 6 digits, 30-second steps. One step of drift is accepted either
// side.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1
	totpIssuer = "OpenPak"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// hotp is RFC 4226 HOTP: dynamic truncation of HMAC-SHA1(secret, counter).
func hotp(secret []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// totpStep is the RFC 6238 time step containing t.
func totpStep(t time.Time) uint64 { return uint64(t.Unix()) / totpPeriod }

// totpMatch returns the step code matches within the skew window, if any,
// comparing in constant time.
func totpMatch(secret []byte, code string, now time.Time) (uint64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	step := totpStep(now)
	var found uint64
	ok := false
	for d := -totpSkew; d <= totpSkew; d++ {
		s := uint64(int64(step) + int64(d))
		if hmac.Equal([]byte(hotp(secret, s, totpDigits)), []byte(code)) {
			found, ok = s, true
		}
	}
	return found, ok
}

// otpauthURI is the key-URI format authenticator apps scan from a QR code.
func otpauthURI(secretB32, accountLabel string) string {
	label := url.PathEscape(totpIssuer + ":" + accountLabel)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// sealer encrypts TOTP secrets at rest with AES-256-GCM. The account id is
// the associated data, so a ciphertext copied onto another account's row
// does not decrypt.
type sealer struct{ aead cipher.AEAD }

// newSealer derives a 256-bit key from secret (ACCOUNT_TOTP_KEY, or the
// session pepper with a domain label when that is unset).
func newSealer(secret string) *sealer {
	key := sha256.Sum256([]byte("openpak-totp-key\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // 32-byte key: cannot fail
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &sealer{aead: aead}
}

func (s *sealer) seal(plain []byte, accountID string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte(accountID)), nil
}

func (s *sealer) open(sealed []byte, accountID string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("identity: sealed secret too short")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], []byte(accountID))
}

// newRecoveryCode is 50 random bits as ten base32 characters, shown as
// "xxxxx-xxxxx".
func newRecoveryCode() (string, error) {
	b := make([]byte, 7)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := strings.ToLower(b32.EncodeToString(b))[:10]
	return s[:5] + "-" + s[5:], nil
}

// normalizeRecoveryCode accepts the code however it was typed.
func normalizeRecoveryCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(code)))
}
