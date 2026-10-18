package identity

import (
	"errors"
	"testing"
)

func TestValidateHandle(t *testing.T) {
	for _, h := range []string{"auroraone", "XboxTest", "Xbox360Test", "WiiTest", "LeiaOrgana", "Scunthorpe", "ScunthorpeX", "DicksonGamer", "AssassinX", "Dickson", "Assassin", "Abcdef123456789"} {
		if err := ValidateHandle(h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	for _, h := range []string{"short", "123456", "a b c d", "AuroraOne_", "AuroraOne-", "AuroraOne.", "ébcdef", " abcdef", "abcdef ", "Abcdef1234567890"} {
		if !errors.Is(ValidateHandle(h), ErrHandleInvalid) {
			t.Errorf("invalid accepted: %s", h)
		}
	}
	for _, h := range []string{"OpenPakSupport", "xBoXLiVe", "Nintendo", "Microsoft", "Support"} {
		if !errors.Is(ValidateHandle(h), ErrHandleReserved) {
			t.Errorf("reserved accepted: %s", h)
		}
	}
	for _, h := range []string{"Fucker", "fUcker123", "FuCkEr", "Fvcker", "Fuuuck123", "xxFuckerxx", "fuckinggamer", "officialfuckerx", "ShitHead", "n1gg3r", "N1gg3r123", "FuckYou"} {
		if !errors.Is(ValidateHandle(h), ErrHandleProfane) {
			t.Errorf("profane accepted: %s", h)
		}
	}
}
