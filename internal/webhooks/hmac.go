package webhooks

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
)

// SignHMAC computes the X-Webhook-Hmac value: a lowercase hex-encoded
// HMAC-SHA512 of the request body using the decrypted webhook secret. SHA-512 is
// fixed (advertised via X-Webhook-Hmac-Algorithm: sha512, §9). The hex encoding
// keeps the header ASCII-safe and is the de-facto convention for webhook
// signatures (consumers recompute and constant-time compare).
func SignHMAC(secret, body []byte) string {
	mac := hmac.New(sha512.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
