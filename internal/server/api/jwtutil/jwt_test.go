package jwtutil

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseAccessRejectsNonAccessTokens(t *testing.T) {
	secret := []byte("test-secret")
	id := uuid.New()

	access, _ := IssueAccess(id, "alice", "admin", secret)
	if _, err := ParseAccess(access, secret); err != nil {
		t.Fatalf("access token rejected: %v", err)
	}

	pending, _ := IssueTOTPPending(id, secret)
	if _, err := ParseAccess(pending, secret); err == nil {
		t.Fatal("totp_pending token accepted as access token")
	}

	enroll, _ := IssueTOTPEnroll(id, "SECRET", secret)
	if _, err := ParseAccess(enroll, secret); err == nil {
		t.Fatal("totp_enroll token accepted as access token")
	}
}
