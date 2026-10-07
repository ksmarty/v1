package push

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 8291 §5 test vector. This pins the whole scheme: if the key derivation,
// the info strings or the record layout are wrong, the body will not match.
func TestEncryptMatchesRFC8291Vector(t *testing.T) {
	const (
		plaintext  = "When I grow up, I want to be a watermelon"
		uaPublic   = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
		asPrivate  = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
		salt       = "DGv6ra1nlYgDCS1FRnbzlw"
		authSecret = "BTBZMqHH6r4Tts7J_aSIgg"
		want       = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	)
	saltBytes, err := decodeB64(salt)
	if err != nil {
		t.Fatal(err)
	}
	privBytes, err := decodeB64(asPrivate)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := ecdh.P256().NewPrivateKey(privBytes)
	if err != nil {
		t.Fatal(err)
	}

	got, err := encryptWith(Subscription{
		Endpoint: "https://push.example.com/send/abc",
		P256dh:   uaPublic,
		Auth:     authSecret,
	}, []byte(plaintext), saltBytes, sender)
	if err != nil {
		t.Fatal(err)
	}
	if encoded := base64.RawURLEncoding.EncodeToString(got); encoded != want {
		t.Fatalf("encrypted body does not match RFC 8291 §5\n got: %s\nwant: %s", encoded, want)
	}
}

func TestEncryptRejectsBadSubscriptions(t *testing.T) {
	valid := Subscription{
		Endpoint: "https://push.example.com/send/abc",
		P256dh:   "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		Auth:     "BTBZMqHH6r4Tts7J_aSIgg",
	}
	cases := map[string]Subscription{
		"empty p256dh": {Endpoint: valid.Endpoint, Auth: valid.Auth},
		"p256dh not a point": {
			Endpoint: valid.Endpoint, P256dh: base64.RawURLEncoding.EncodeToString([]byte("nope")), Auth: valid.Auth,
		},
		"auth wrong length": {
			Endpoint: valid.Endpoint, P256dh: valid.P256dh,
			Auth: base64.RawURLEncoding.EncodeToString([]byte("short")),
		},
	}
	for name, sub := range cases {
		if _, err := Encrypt(sub, []byte("hi")); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	// The valid one must succeed.
	if _, err := Encrypt(valid, []byte("hi")); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
}

func TestVAPIDKeysRoundTrip(t *testing.T) {
	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ParseVAPIDKeys(keys.PrivateScalar())
	if err != nil {
		t.Fatal(err)
	}
	if restored.PublicKey() != keys.PublicKey() {
		t.Fatalf("public key changed across a round trip: %q vs %q", restored.PublicKey(), keys.PublicKey())
	}
	// A restored key must still sign verifiably, which is what proves the
	// private scalar survived and not just the public point.
	auth, err := restored.authorization("https://push.example.com/send/abc", "mailto:v1@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVAPID(auth, &keys.Private.PublicKey); err != nil {
		t.Fatalf("restored key produced an unverifiable header: %v", err)
	}
	// Junk must be rejected rather than producing a zero scalar.
	for _, bad := range []string{"", "!!!!", base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		if _, err := ParseVAPIDKeys(bad); err == nil {
			t.Fatalf("ParseVAPIDKeys(%q) should have failed", bad)
		}
	}
}

// verifyVAPID checks the ES256 JWT in a VAPID header, including that the
// signature is the raw 64-byte r||s form JWS requires (not DER).
func verifyVAPID(header string, pub *ecdsa.PublicKey) error {
	rest := strings.TrimPrefix(header, "vapid ")
	parts := strings.Split(rest, ", ")
	if len(parts) != 2 {
		return errBad("header is not 'vapid t=..., k=...'")
	}
	jwt := strings.TrimPrefix(parts[0], "t=")
	key := strings.TrimPrefix(parts[1], "k=")

	segments := strings.Split(jwt, ".")
	if len(segments) != 3 {
		return errBad("jwt does not have three segments")
	}
	sig, err := decodeB64(segments[2])
	if err != nil {
		return err
	}
	if len(sig) != 64 {
		return errBad("signature is not 64 bytes (r||s)")
	}
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return errBad("signature does not verify")
	}
	if key == "" {
		return errBad("no public key in the header")
	}
	return nil
}

func TestVAPIDAuthorizationClaims(t *testing.T) {
	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0)
	header, err := keys.authorization("https://fcm.googleapis.com/fcm/send/abc", "mailto:v1@example.com", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVAPID(header, &keys.Private.PublicKey); err != nil {
		t.Fatalf("header does not verify: %v", err)
	}
	if !strings.Contains(header, "k="+keys.PublicKey()) {
		t.Fatal("header must carry the application server public key")
	}
	claimsRaw, err := decodeB64(strings.Split(strings.TrimPrefix(strings.Split(header, ", ")[0], "vapid t="), ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	// The audience is the push service origin, not the full endpoint path.
	if claims.Aud != "https://fcm.googleapis.com" {
		t.Fatalf("aud = %q, want the origin only", claims.Aud)
	}
	if claims.Sub != "mailto:v1@example.com" {
		t.Fatalf("sub = %q", claims.Sub)
	}
	if got := time.Unix(claims.Exp, 0).Sub(at); got <= 0 || got > 24*time.Hour {
		t.Fatalf("exp is %s after issue, want within (0, 24h]", got)
	}
}

func TestSendSetsHeadersAndReportsGone(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(gotBody)
		w.WriteHeader(status)
	}))
	defer srv.Close()

	keys, err := GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(keys, "mailto:v1@example.com")
	sub := Subscription{
		Endpoint: srv.URL + "/send/abc",
		P256dh:   "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		Auth:     "BTBZMqHH6r4Tts7J_aSIgg",
	}
	msg := Message{Title: "v1", Body: "turn finished", URL: "/project/p1"}

	if err := client.Send(context.Background(), sub, msg); err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if enc := gotHeaders.Get("Content-Encoding"); enc != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q", enc)
	}
	if ttl := gotHeaders.Get("TTL"); ttl != "43200" {
		t.Fatalf("TTL = %q, want 43200", ttl)
	}
	if u := gotHeaders.Get("Urgency"); u != "high" {
		t.Fatalf("Urgency = %q, want high", u)
	}
	if !strings.HasPrefix(gotHeaders.Get("Authorization"), "vapid t=") {
		t.Fatalf("Authorization = %q", gotHeaders.Get("Authorization"))
	}
	// The body must be a real aes128gcm record: salt(16) + rs(4) + idlen(1) +
	// keyid(65) + at least the GCM tag and delimiter.
	if len(gotBody) < 16+4+1+65+17 {
		t.Fatalf("body is only %d bytes, too short to be a record", len(gotBody))
	}
	if gotBody[20] != 65 {
		t.Fatalf("idlen = %d, want 65", gotBody[20])
	}

	// A dropped subscription must be reported as such, so the caller can prune.
	for _, gone := range []int{http.StatusNotFound, http.StatusGone} {
		status = gone
		if err := client.Send(context.Background(), sub, msg); err != ErrGone {
			t.Fatalf("status %d: err = %v, want ErrGone", gone, err)
		}
	}
	status = http.StatusInternalServerError
	if err := client.Send(context.Background(), sub, msg); err == nil || err == ErrGone {
		t.Fatalf("status 500: err = %v, want a plain error", err)
	}
}

type errBad string

func (e errBad) Error() string { return string(e) }
