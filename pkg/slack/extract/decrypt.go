package extract

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	chromeSalt    = "saltysalt"
	aesKeyLen     = 16
	macIterations = 1003
	linuxIterations = 1
)

// decryptCookieValue decrypts a Chromium-encrypted cookie blob.
func decryptCookieValue(encrypted []byte) (string, error) {
	// Strip "v10" or "v11" prefix (3 bytes)
	if len(encrypted) < 3 {
		return "", fmt.Errorf("encrypted value too short (%d bytes)", len(encrypted))
	}
	prefix := string(encrypted[:3])
	if prefix != "v10" && prefix != "v11" {
		return "", fmt.Errorf("unexpected encryption prefix %q", prefix)
	}
	ciphertext := encrypted[3:]

	// Get passphrase from OS keystore
	passphrase, err := getPassphrase()
	if err != nil {
		return "", fmt.Errorf("get passphrase: %w", err)
	}

	// Derive AES key via PBKDF2
	iterations := macIterations
	if runtime.GOOS == "linux" {
		iterations = linuxIterations
	}
	key := pbkdf2.Key(passphrase, []byte(chromeSalt), iterations, aesKeyLen, sha1.New)

	// Decrypt AES-128-CBC with IV = 16 space bytes
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext length %d not a multiple of block size %d", len(ciphertext), aes.BlockSize)
	}

	iv := make([]byte, aes.BlockSize)
	for i := range iv {
		iv[i] = 0x20 // space character
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove PKCS#7 padding
	plaintext, err = removePKCS7Padding(plaintext)
	if err != nil {
		return "", fmt.Errorf("remove padding: %w", err)
	}

	// Chromium DB version ≥24 prepends a 32-byte SHA-256 hash of the host_key.
	// Find the actual cookie value by looking for the xoxd- prefix.
	result := string(plaintext)
	if idx := strings.Index(result, "xoxd-"); idx > 0 {
		result = result[idx:]
	}

	return result, nil
}

// getPassphrase retrieves the Slack encryption passphrase from the OS keystore.
func getPassphrase() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("security", "find-generic-password",
			"-s", "Slack Safe Storage", "-w").Output()
		if err != nil {
			return nil, fmt.Errorf("keychain access failed (you may need to allow access in the dialog): %w", err)
		}
		return []byte(strings.TrimSpace(string(out))), nil

	case "linux":
		// Try secret-tool (GNOME Keyring / Secret Service)
		out, err := exec.Command("secret-tool", "lookup", "application", "Slack").Output()
		if err == nil && len(out) > 0 {
			return []byte(strings.TrimSpace(string(out))), nil
		}
		// Fallback: Chromium uses "peanuts" when no keyring is available
		return []byte("peanuts"), nil

	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// removePKCS7Padding removes PKCS#7 padding from decrypted data.
func removePKCS7Padding(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(data) {
		return nil, fmt.Errorf("invalid padding length %d", padLen)
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, fmt.Errorf("invalid padding byte at position %d", i)
		}
	}
	return data[:len(data)-padLen], nil
}
