package webhookserver_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/concourse/concourse/atc/api/webhookserver"
)

func TestValidateHMACSHA256_Valid(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !webhookserver.ValidateHMACSHA256(secret, body, signature) {
		t.Error("expected valid HMAC signature to pass validation")
	}
}

func TestValidateHMACSHA256_Invalid(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)
	signature := "sha256=0000000000000000000000000000000000000000000000000000000000000000"

	if webhookserver.ValidateHMACSHA256(secret, body, signature) {
		t.Error("expected invalid HMAC signature to fail validation")
	}
}

func TestValidateHMACSHA256_WrongPrefix(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)
	signature := "sha1=abc123"

	if webhookserver.ValidateHMACSHA256(secret, body, signature) {
		t.Error("expected wrong prefix to fail validation")
	}
}

func TestValidateHMACSHA256_InvalidHex(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)
	signature := "sha256=not-hex"

	if webhookserver.ValidateHMACSHA256(secret, body, signature) {
		t.Error("expected invalid hex to fail validation")
	}
}

func TestValidateHMACSHA256_TamperedBody(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Tamper with the body
	tamperedBody := []byte(`{"ref":"refs/heads/evil"}`)

	if webhookserver.ValidateHMACSHA256(secret, tamperedBody, signature) {
		t.Error("expected tampered body to fail validation")
	}
}
