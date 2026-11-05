package identity

import (
	"strings"
	"testing"
)

func TestCheckPasswordPeppers(t *testing.T) {
	oldP, newP := strings.Repeat("o", 32), strings.Repeat("n", 32)
	hOld, _ := hashPassword(oldP, "correct horse")
	hNew, _ := hashPassword(newP, "correct horse")

	// before the cutover there is one pepper and nothing is ever re-hashed
	single := &Service{pepper: oldP}
	if ok, re := single.checkPassword(hOld, 0, "correct horse"); !ok || re != "" {
		t.Fatalf("single pepper: ok=%v rehash=%v", ok, re != "")
	}

	s := &Service{pepper: oldP}
	s.SetNewPepper(newP)
	if s.pepper != newP || s.oldPepper != oldP || s.pepperGen != 1 {
		t.Fatal("SetNewPepper did not swap the peppers")
	}
	// an old-pepper hash (generation 0) verifies and asks to be re-hashed under the new pepper
	ok, re := s.checkPassword(hOld, 0, "correct horse")
	if !ok || re == "" {
		t.Fatalf("old hash: ok=%v rehash=%v", ok, re != "")
	}
	if !comparePassword(re, newP, "correct horse") || comparePassword(re, oldP, "correct horse") {
		t.Fatal("the replacement hash is not under the new pepper")
	}
	// a new-pepper hash (generation 1) verifies and is left alone
	if ok, re := s.checkPassword(hNew, 1, "correct horse"); !ok || re != "" {
		t.Fatalf("new hash: ok=%v rehash=%v", ok, re != "")
	}
	// wrong password fails under both generations, and a hash is never read under the other pepper
	if ok, _ := s.checkPassword(hOld, 0, "wrong horse"); ok {
		t.Fatal("wrong password accepted on an old hash")
	}
	if ok, _ := s.checkPassword(hNew, 1, "wrong horse"); ok {
		t.Fatal("wrong password accepted on a new hash")
	}
	if ok, _ := s.checkPassword(hOld, 1, "correct horse"); ok {
		t.Fatal("an old-pepper hash labelled as the new generation must not verify")
	}
	// tokens: both forms are looked up, the current one is stored
	th := s.tokenHashes("tok")
	if len(th) != 2 || th[0] != hashToken("tok", newP) || th[1] != hashToken("tok", oldP) {
		t.Fatal("tokenHashes")
	}
}
