package emaillink

import "testing"

func TestBuild(t *testing.T) {
	cases := []struct{ api, site, path, want string }{
		{"http://localhost:8000", "", Verify, "http://localhost:8000/v1/verify?token=t"},
		{"http://localhost:8000", "https://openpak.org", Verify, "https://openpak.org/verify?token=t"},
		{"http://localhost:8000", "https://openpak.org/", ResetPassword, "https://openpak.org/reset-password?token=t"},
	}
	for _, c := range cases {
		if got := Build(c.api, c.site, c.path, "t"); got != c.want {
			t.Errorf("Build(%q,%q,%q) = %q, want %q", c.api, c.site, c.path, got, c.want)
		}
	}
}
