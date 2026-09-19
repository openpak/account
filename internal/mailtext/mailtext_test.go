package mailtext

import (
	"strings"
	"testing"
)

func TestEveryLanguageHasEveryMail(t *testing.T) {
	for lang, m := range mails {
		for kind, en := range mails["en"] {
			got, ok := m[kind]
			if !ok || got.subject == "" {
				t.Errorf("%s: no %s", lang, kind)
			}
			if strings.Contains(en.body, "{link}") != strings.Contains(got.body, "{link}") {
				t.Errorf("%s %s: {link} placeholder differs", lang, kind)
			}
		}
	}
}

func TestLangAndGet(t *testing.T) {
	for header, want := range map[string]string{"": "en", "ja": "en", "de": "de", "pt-PT": "pt-BR", "ja, fr-CA;q=0.8": "fr", "pt-BR": "pt-BR"} {
		if got := Lang(header); got != want {
			t.Errorf("Lang(%q) = %q, want %q", header, got, want)
		}
	}
	if _, body := Get("de", Verify, "", "https://x/v"); !strings.HasPrefix(body, "Hallo,\n") || !strings.Contains(body, "https://x/v") {
		t.Errorf("body %q", body)
	}
	if _, body := Get("en", Verify, "Ana", "l"); !strings.HasPrefix(body, "Hi Ana,") {
		t.Errorf("body %q", body)
	}
}
