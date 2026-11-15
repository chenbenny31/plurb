package auth

import "testing"

func TestHashVerify(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(h, "correct horse battery") {
		t.Error("right password rejected")
	}
	if Verify(h, "correct horse batterY") {
		t.Error("wrong password accepted")
	}
	if Verify("", "anything") || Verify("bcrypt$x", "anything") {
		t.Error("empty or foreign hash accepted")
	}
	if _, err := Hash("short"); err != ErrWeakPassword {
		t.Errorf("short password: err = %v", err)
	}
}
