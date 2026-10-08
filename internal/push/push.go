// Package push implements Web Push delivery: RFC 8291 payload encryption
// (aes128gcm) and RFC 8292 VAPID authentication.
//
// Standard library only. The whole implementation is a few hundred lines of
// well-specified crypto, and AGENTS.md asks for a stated reason before adding a
// non-stdlib Go dependency, so there is no reason to take one here.
//
// Web Push is what makes a notification arrive while the app is closed: the
// page cannot run in the background (iOS suspends it within seconds), so the
// push service wakes the service worker instead.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// recordSize is the aes128gcm record size (rs) advertised in each message. It
// bounds the plaintext, not the ciphertext.
const recordSize = 4096

// MaxPayload is the largest plaintext Encrypt accepts: one record minus the
// GCM tag and the padding delimiter.
const MaxPayload = recordSize - 16 - 1

// ErrGone reports that the push service no longer knows the subscription
// (HTTP 404 or 410). The caller should delete it rather than retry.
var ErrGone = errors.New("push: subscription is gone")

// DefaultTTL is how long the push service may hold a message for a device that
// is offline. A day is long enough to survive a phone being asleep and short
// enough that a stale turn notification does not surface much later.
const DefaultTTL = 12 * 60 * 60

// Subscription is a browser push subscription, as produced by
// PushSubscription.toJSON().
type Subscription struct {
	Endpoint string
	// P256dh is the client's public key: base64url, uncompressed P-256 point.
	P256dh string
	// Auth is the 16-byte authentication secret, base64url.
	Auth string
}

// Message is the JSON payload handed to the service worker.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// Encrypt encrypts payload for sub as a single aes128gcm record (RFC 8291).
func Encrypt(sub Subscription, payload []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("push: salt: %w", err)
	}
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: sender key: %w", err)
	}
	return encryptWith(sub, payload, salt, sender)
}

// encryptWith is Encrypt with the salt and sender key supplied, so the RFC 8291
// test vector can be reproduced exactly.
func encryptWith(sub Subscription, payload, salt []byte, sender *ecdh.PrivateKey) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("push: payload is %d bytes, max %d", len(payload), MaxPayload)
	}
	uaRaw, err := decodeB64(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("push: p256dh: %w", err)
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, fmt.Errorf("push: p256dh is not a P-256 point: %w", err)
	}
	auth, err := decodeB64(sub.Auth)
	if err != nil {
		return nil, fmt.Errorf("push: auth: %w", err)
	}
	if len(auth) != 16 {
		return nil, fmt.Errorf("push: auth is %d bytes, want 16", len(auth))
	}
	shared, err := sender.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("push: ecdh: %w", err)
	}
	asPub := sender.PublicKey().Bytes()

	// RFC 8291 §3.4. The info string is length-prefixed by nothing — it is the
	// literal prefix, a NUL, then both uncompressed points.
	keyInfo := make([]byte, 0, 14+len(uaRaw)+len(asPub))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaRaw...)
	keyInfo = append(keyInfo, asPub...)

	ikm, err := hkdf.Key(sha256.New, shared, auth, string(keyInfo), 32)
	if err != nil {
		return nil, fmt.Errorf("push: ikm: %w", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("push: prk: %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("push: cek: %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("push: nonce: %w", err)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("push: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("push: gcm: %w", err)
	}
	// The last record's plaintext carries a 0x02 delimiter (RFC 8188 §2).
	plain := make([]byte, 0, len(payload)+1)
	plain = append(plain, payload...)
	plain = append(plain, 2)
	sealed := gcm.Seal(nil, nonce, plain, nil)

	// Body: salt || rs || idlen || keyid || ciphertext.
	body := make([]byte, 0, 16+4+1+len(asPub)+len(sealed))
	body = append(body, salt...)
	var rs [4]byte
	binary.BigEndian.PutUint32(rs[:], recordSize)
	body = append(body, rs[:]...)
	body = append(body, byte(len(asPub)))
	body = append(body, asPub...)
	body = append(body, sealed...)
	return body, nil
}

// VAPIDKeys is the application server key pair that authenticates pushes
// (RFC 8292). One pair is enough for the whole server.
type VAPIDKeys struct {
	Private *ecdsa.PrivateKey
}

// GenerateVAPIDKeys creates a fresh P-256 key pair.
func GenerateVAPIDKeys() (*VAPIDKeys, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: vapid key: %w", err)
	}
	return &VAPIDKeys{Private: priv}, nil
}

// PrivateScalar is the raw private scalar, base64url. This is what gets stored
// in settings; the public key is derived from it.
func (k *VAPIDKeys) PrivateScalar() string {
	return base64.RawURLEncoding.EncodeToString(padLeft(k.Private.D.Bytes(), 32))
}

// PublicKey is the uncompressed public point, base64url. This is the
// applicationServerKey the browser subscribes with.
func (k *VAPIDKeys) PublicKey() string {
	pub, err := k.Private.ECDH()
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(pub.PublicKey().Bytes())
}

// ParseVAPIDKeys rebuilds the pair from the stored private scalar.
func ParseVAPIDKeys(scalarB64 string) (*VAPIDKeys, error) {
	raw, err := decodeB64(scalarB64)
	if err != nil {
		return nil, fmt.Errorf("push: vapid scalar: %w", err)
	}
	curve := elliptic.P256()
	d := new(big.Int).SetBytes(raw)
	if d.Sign() == 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("push: vapid scalar out of range")
	}
	x, y := curve.ScalarBaseMult(raw)
	return &VAPIDKeys{Private: &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
		D:         d,
	}}, nil
}

// authorization builds the VAPID Authorization header (RFC 8292 §3): an ES256
// JWT whose audience is the push service's origin.
func (k *VAPIDKeys) authorization(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("push: endpoint: %w", err)
	}
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	if err != nil {
		return "", fmt.Errorf("push: claims: %w", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.Private, digest[:])
	if err != nil {
		return "", fmt.Errorf("push: sign: %w", err)
	}
	// JWS wants the raw r||s pair, not the DER encoding ecdsa.SignASN1 gives.
	sig := append(padLeft(r.Bytes(), 32), padLeft(s.Bytes(), 32)...)

	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	return "vapid t=" + jwt + ", k=" + k.PublicKey(), nil
}

// Client delivers Web Push messages.
type Client struct {
	Keys *VAPIDKeys
	// Subject is the push service's contact for us: a mailto: or https: URL.
	// RFC 8292 requires one.
	Subject string
	HTTP    *http.Client
}

// NewClient builds a sender.
func NewClient(keys *VAPIDKeys, subject string) *Client {
	return &Client{
		Keys:    keys,
		Subject: subject,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Send encrypts msg and posts it to sub. It returns ErrGone when the push
// service has dropped the subscription.
func (c *Client) Send(ctx context.Context, sub Subscription, msg Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("push: payload: %w", err)
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	auth, err := c.Keys.authorization(sub.Endpoint, c.Subject, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("push: request: %w", err)
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(DefaultTTL))
	// The turn is blocked until the user acts, so deliver immediately rather
	// than letting the service batch it.
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", auth)

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("push: send: %w", err)
	}
	defer res.Body.Close()
	// Read a little so the connection can be reused, but never a large error
	// page. The body carries the reason: Apple answers a rejected VAPID token
	// with {"reason":"BadJwtToken"}, which is the only way to tell a bad key
	// from a bad claim.
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 4096))

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	default:
		if reason := strings.TrimSpace(string(detail)); reason != "" {
			return fmt.Errorf("send: %s: %s", res.Status, reason)
		}
		return fmt.Errorf("send: %s", res.Status)
	}
}

// decodeB64 accepts the unpadded base64url browsers produce, and tolerates
// padded and standard-alphabet variants.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty")
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("%q is not valid base64", s)
}

// padLeft left-pads b with zeros to n bytes.
func padLeft(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}
