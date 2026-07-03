package auth

import "testing"

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password verified true")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("hashes of the same password must differ (random salt)")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	if _, err := VerifyPassword("not-a-hash", "x"); err == nil {
		t.Fatal("expected error for malformed hash")
	}
}
