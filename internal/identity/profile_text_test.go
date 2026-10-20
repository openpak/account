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
