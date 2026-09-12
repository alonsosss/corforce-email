package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP (RFC 6238) mandates SHA-1
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	digits   = 6
	period   = 30
	skewStep = 1 // ±1 window → 90-second tolerance
)

// GenerateSecret returns a cryptographically random base32-encoded secret (20 bytes / 160 bits).
func GenerateSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("totp: generate secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// Generate returns the TOTP code for the given secret at time t.
func Generate(secret string, t time.Time) (string, error) {
	code, err := hotp(secret, uint64(t.Unix()/period))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, code), nil
}

// Validate checks the provided code against the secret at the current time,
// accepting codes from [now-skewStep ... now+skewStep] windows.
func Validate(secret, code string) bool {
	now := time.Now().UTC()
	counter := uint64(now.Unix() / period)
	for delta := -int64(skewStep); delta <= int64(skewStep); delta++ {
		c, err := hotp(secret, uint64(int64(counter)+delta))
		if err != nil {
			return false
		}
		candidate := fmt.Sprintf("%0*d", digits, c)
		if hmac.Equal([]byte(candidate), []byte(code)) {
			return true
		}
	}
	return false
}

// ProvisioningURI returns the otpauth:// URI used to generate a QR code.
func ProvisioningURI(secret, accountName, issuer string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprintf("%d", digits))
	v.Set("period", fmt.Sprintf("%d", period))
	label := url.PathEscape(issuer + ":" + accountName)
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// hotp computes an HOTP value (RFC 4226) for the given key and counter.
func hotp(secret string, counter uint64) (int, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return 0, fmt.Errorf("totp: decode secret: %w", err)
	}
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)
	mac := hmac.New(sha1.New, key) //nolint:gosec // required by RFC 4226
	mac.Write(msg)
	h := mac.Sum(nil)
	offset := h[len(h)-1] & 0x0F
	binCode := (int(h[offset])&0x7F)<<24 |
		int(h[offset+1])<<16 |
		int(h[offset+2])<<8 |
		int(h[offset+3])
	return binCode % int(math.Pow10(digits)), nil
}
