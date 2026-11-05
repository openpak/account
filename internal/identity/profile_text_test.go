package identity

import (
	"strings"
	"testing"
)

func TestProfileTextLimitsAndClearing(t *testing.T) {
	for _, tc := range []struct {
		bio, motto string
		valid      bool
	}{
		{"", "", true},
		{strings.Repeat("a", 499), strings.Repeat("a", 21), true},
		{strings.Repeat("a", 500), "", false},
		{"", strings.Repeat("a", 22), false},
		{"two\nlines", "one line", true},
		{"", "two\nlines", false},
		{"embedded\x00NUL", "", false},
		{"", strings.Repeat("😀", 10) + "a", true},
		{"", strings.Repeat("😀", 11), false},
	} {
		err := validateProfileText(&tc.bio, &tc.motto)
		if (err == nil) != tc.valid {
			t.Errorf("bio=%q motto=%q: %v", tc.bio, tc.motto, err)
		}
	}
	if err := validateProfileText(nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLanguageAndTimezoneValidation(t *testing.T) {
	for in, want := range map[string]string{"": "", "en": "en", "EN": "en", "pt-br": "pt-BR", "zh-hant-tw": "zh-Hant-TW", " fr ": "fr"} {
		if got, err := NormalizeLanguage(in); err != nil || got != want {
			t.Errorf("language %q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"e", "english1", "en_US", "en-", "x", "en-\x00", strings.Repeat("a", 21), "12"} {
		if _, err := NormalizeLanguage(in); err == nil {
			t.Errorf("language %q accepted", in)
		}
	}
	for _, in := range []string{"", "UTC", "Europe/Dublin", "America/Argentina/Buenos_Aires"} {
		if _, err := ValidateTimezone(in); err != nil {
			t.Errorf("timezone %q: %v", in, err)
		}
	}
	for _, in := range []string{"Mars/Base", "Local", "../etc/passwd", "Europe/Du\nblin", strings.Repeat("a", 65)} {
		if _, err := ValidateTimezone(in); err == nil {
			t.Errorf("timezone %q accepted", in)
		}
	}
}
