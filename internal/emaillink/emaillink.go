// Package emaillink builds the links that go into account emails.
//
// The core serves its own /v1/verify and /v1/reset-password, but in a deployment
// where users meet the website rather than the core (the core stays on an internal
// network), the mailed link has to point at the site's equivalent routes instead.
// ACCOUNT_EMAIL_LINK_BASE selects that; unset keeps the core's own URLs.
package emaillink

import "strings"

// Path values, matching both the core's /v1 routes and the website's top-level ones.
const (
	Verify        = "/verify"
	ResetPassword = "/reset-password"
	ConfirmEmail  = "/email/confirm"
)

// Build returns the emailed link for path. With siteBase set the link points at the
// site (siteBase + path); otherwise at the core's own API (apiBase + /v1 + path).
func Build(apiBase, siteBase, path, token string) string {
	base, prefix := apiBase, "/v1"
	if siteBase != "" {
		base, prefix = siteBase, ""
	}
	return strings.TrimRight(base, "/") + prefix + path + "?token=" + token
}
