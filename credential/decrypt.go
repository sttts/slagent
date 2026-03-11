package credential

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
func decryptCookieValue(encrypted []byte, isSnap bool) (string, error) {
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
	passphrase, err := getPassphrase(isSnap)
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
func getPassphrase(isSnap bool) ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("security", "find-generic-password",
			"-s", "Slack Safe Storage", "-w").Output()
		if err != nil {
			return nil, fmt.Errorf("keychain access failed (you may need to allow access in the dialog): %w", err)
		}
		return []byte(strings.TrimSpace(string(out))), nil

	case "linux":
		// For Snap installations, the keyring is isolated inside the Snap's
		// confinement. We must query it from inside the Snap environment.
		if isSnap {
			out, err := getSnapPassphrase()
			if err == nil && len(out) > 0 {
				return out, nil
			}
		}

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

// getSnapPassphrase retrieves the Slack OSCrypt password from inside the Snap's
// isolated GNOME Keyring by running a Python script within the Snap confinement.
func getSnapPassphrase() ([]byte, error) {
	script := `import ctypes,ctypes.util,sys
ls=ctypes.CDLL(ctypes.util.find_library("secret-1"))
class A(ctypes.Structure):
 _fields_=[("n",ctypes.c_char_p),("t",ctypes.c_int)]
class S(ctypes.Structure):
 _fields_=[("n",ctypes.c_char_p),("f",ctypes.c_int),("a",A*32)]
s=S()
s.n=b"chrome_libsecret_os_crypt_password_v2"
s.f=2
s.a[0]=A(b"application",0)
s.a[1]=A(None,0)
ls.secret_password_lookup_sync.restype=ctypes.c_void_p
e=ctypes.c_void_p(0)
r=ls.secret_password_lookup_sync(ctypes.byref(s),None,ctypes.byref(e),b"application",b"Slack",None)
if r:sys.stdout.write(ctypes.string_at(r).decode("utf-8"))
else:sys.exit(1)`

	out, err := exec.Command("snap", "run", "--shell", "slack.slack", "-c",
		"python3 -c '"+script+"'").Output()
	if err != nil {
		return nil, fmt.Errorf("snap passphrase extraction failed: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("snap passphrase extraction returned empty result")
	}
	return []byte(strings.TrimSpace(string(out))), nil
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
