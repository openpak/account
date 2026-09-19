# Two-factor authentication

TOTP per RFC 6238: HMAC-SHA1, 6 digits, 30-second steps, one step of drift
either side. Standard library only. Ten single-use recovery codes per
enrolment.

## Web flow (HTTP, session cookie)

| Route | Body | Answer |
| --- | --- | --- |
| `POST /v1/2fa/setup` | `{"password"}` | `200 {"secret","otpauth_uri"}`; 403 wrong password; 409 already enabled |
| `POST /v1/2fa/enable` | `{"code"}` | `200 {"recovery_codes":[10]}`, shown once; 401 wrong code; 409 no pending setup |
| `POST /v1/2fa/disable` | `{"password","code"}` (`code` may be a recovery code) | `200`; 403; 401; 409 not enabled |
| `POST /v1/login` | `{"email","password"}` | with 2FA: `202 {"two_factor_required":true,"challenge"}` and **no cookie** |
| `POST /v1/login/2fa` | `{"challenge","code"}` or `{"challenge","recovery_code"}` | `200` + cookie; 401 wrong code or dead challenge |

Setup asks for the password even though the caller is signed in. Otherwise a
borrowed session could tie the account to someone else's phone and lock the
owner out. Calling setup again before enabling replaces the pending secret.

## Rate limits

- Per account: 5 wrong codes within 15 minutes lock the second factor for
  what is left of that window. Every path counts toward this and every path
  is locked by it: sign-in, enable, disable and gRPC `VerifySecondFactor`.
  The answer is HTTP 429 with `Retry-After: 900`, or gRPC `ResourceExhausted`.
- Per challenge: a challenge lasts 10 minutes, is single-use, and is burned
  after 5 wrong codes.
- Replay: a TOTP code is accepted only for a time step newer than the last one
  accepted, so a code that was seen once cannot be used again.

## The gRPC rule: a password alone never signs in a 2FA account

Adapters sign consoles in with the web password: the Switch adapter uses
`VerifyPassword`, and the Wii U adapter uses `VerifyPasswordByID`. If those
calls only reported a flag, every caller written before 2FA existed would
ignore it and become a bypass. So the calls fail closed:

1. The password is checked first. A wrong one is `Unauthenticated` as before,
   so the rule tells a guesser nothing.
2. If the account has 2FA and the request did not set
   `second_factor_supported = true`, the answer is `FailedPrecondition
   "two-factor authentication required"`, with no account id.
3. If the caller did set it, the answer is the usual one with
   `two_factor_required = true`. The caller **must** then succeed at
   `Identity.VerifySecondFactor(account_id, code | recovery_code)` before it
   treats the person as signed in or links anything.

Consequences:

- Consoles cannot prompt for a TOTP code, so a console password sign-in for a
  2FA account fails until that adapter runs the second factor itself (the
  Switch link page on a phone can) and then gives the device an adapter
  credential. `VerifyAdapterCredential` is not affected: a device credential
  is issued after a completed sign-in, like an app password.
- The website's own sign-in goes through `POST /v1/login`, which enforces 2FA
  itself. With `GET /v1/whoami` the website no longer needs `VerifyPassword`
  at sign-in at all. For a password re-check inside a session that already
  passed 2FA (for example, before deleting the account), the website sets
  `second_factor_supported = true` and may ignore `two_factor_required`.

## Secrets at rest

The raw 20-byte TOTP secret is sealed with AES-256-GCM. The account id is the
associated data, so a ciphertext copied onto another row does not decrypt.
The key is SHA-256 of `ACCOUNT_TOTP_KEY` (at least 32 bytes). If that is
unset, the key is derived from `ACCOUNT_SESSION_SECRET`, which is already a
secret the core cannot rotate without invalidating every password. Changing
the key makes every enrolled second factor unreadable, and affected users then
sign in with a recovery code. Recovery codes are stored as peppered SHA-256
hashes, like every other token.
