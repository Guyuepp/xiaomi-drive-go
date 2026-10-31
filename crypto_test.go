package micloud

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// TestAESCBCMatchesOpenSSL pins the AES-128-CBC + PKCS7 encoding to the exact
// bytes the web client (and the probe's openssl) produce, so SMS-login "user"
// encryption stays wire-compatible.
func TestAESCBCMatchesOpenSSL(t *testing.T) {
	// openssl enc -aes-128-cbc -K <hex "0123456789abcdef"> -iv <hex "0102030405060708">
	key := []byte("0123456789abcdef")
	ct, err := aesCBCEncrypt(key, loginIV, []byte("13800138000"))
	if err != nil {
		t.Fatal(err)
	}
	got := base64.StdEncoding.EncodeToString(ct)
	const want = "zjd88HsPWgCSUbRZjZn8og=="
	if got != want {
		t.Fatalf("AES-CBC mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestEUIShape checks the eui field layout: <base64 rsa>.<base64 fieldname>.
func TestEUIShape(t *testing.T) {
	eui, enc, err := encryptLoginField("user", "13800138000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base64.StdEncoding.DecodeString(enc); err != nil {
		t.Fatalf("encrypted value not base64: %v", err)
	}
	parts := strings.Split(eui, ".")
	if len(parts) != 2 {
		t.Fatalf("eui should have two dot-separated parts, got %d", len(parts))
	}
	name, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || string(name) != "user" {
		t.Fatalf("eui field-name part = %q (err %v), want base64(\"user\")", name, err)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[0]); err != nil {
		t.Fatalf("eui rsa part not base64: %v", err)
	}
}

func TestValidateName(t *testing.T) {
	ok := []string{"a.txt", ".hidden", "end.", "CON", "名字 空格 中间", "🙂"}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", ".", "..", "...", " leading", "trailing ", "a/b", "a:b", "a*b", `a"b`, "a\tb"}
	for _, n := range bad {
		if err := ValidateName(n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", n, err)
		}
	}
	if err := ValidateName(strings.Repeat("a", 201)); !errors.Is(err, ErrInvalidName) {
		t.Errorf("201 chars should be rejected, got %v", err)
	}
	if err := ValidateName(strings.Repeat("中", 127)); err != nil {
		t.Errorf("127 CJK chars should pass, got %v", err)
	}
	if err := ValidateName(strings.Repeat("中", 128)); !errors.Is(err, ErrInvalidName) {
		t.Errorf("128 CJK chars should be rejected (byte limit), got %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := SanitizeName("a/b:c*?.txt"); strings.ContainsAny(got, illegalNameChars) {
		t.Errorf("SanitizeName left illegal chars: %q", got)
	}
	for _, reserved := range []string{"", ".", "..", "..."} {
		if got := SanitizeName(reserved); got == reserved {
			t.Errorf("SanitizeName(%q) should not return a reserved name", reserved)
		}
	}
}
