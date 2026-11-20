package crypto

import (
	"bytes"
	"testing"
)

func TestCipherEncryptDecrypt(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	enc, err := c.EncryptString("super-secret-value")
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	if enc == "super-secret-value" || enc == "" {
		t.Fatalf("expected encrypted ciphertext, got %q", enc)
	}

	dec, err := c.DecryptString(enc)
	if err != nil {
		t.Fatalf("DecryptString: %v", err)
	}
	if dec != "super-secret-value" {
		t.Fatalf("expected original plaintext, got %q", dec)
	}

	// Empty string roundtrip
	if empty, err := c.EncryptString(""); err != nil || empty != "" {
		t.Fatalf("expected empty roundtrip, got %q, %v", empty, err)
	}
}

func TestPassphraseBundleRoundtrip(t *testing.T) {
	payload := []byte(`{"upstreams":[{"name":"s3"}]}`)
	blob, err := EncryptWithPassphrase(payload, "correct-horse-battery")
	if err != nil {
		t.Fatalf("EncryptWithPassphrase: %v", err)
	}

	out, err := DecryptWithPassphrase(blob, "correct-horse-battery")
	if err != nil {
		t.Fatalf("DecryptWithPassphrase: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("payload mismatch: %s", out)
	}

	if _, err := DecryptWithPassphrase(blob, "wrong-passphrase"); err == nil {
		t.Fatal("expected error with wrong passphrase")
	}

	// Empty passphrase is also supported
	emptyBlob, err := EncryptWithPassphrase(payload, "")
	if err != nil {
		t.Fatalf("EncryptWithPassphrase empty: %v", err)
	}
	emptyOut, err := DecryptWithPassphrase(emptyBlob, "")
	if err != nil || !bytes.Equal(emptyOut, payload) {
		t.Fatalf("DecryptWithPassphrase empty mismatch: err=%v out=%s", err, emptyOut)
	}

	// Short non-empty passphrase must still fail
	if _, err := EncryptWithPassphrase(payload, "12345"); err == nil {
		t.Fatal("expected error for 5-char passphrase")
	}
}

func TestAPIKeyGeneration(t *testing.T) {
	plain, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if len(plain) < 20 || prefix == "" || hash != HashAPIKey(plain) {
		t.Fatalf("unexpected api key output: plain=%q prefix=%q hash=%q", plain, prefix, hash)
	}
}
