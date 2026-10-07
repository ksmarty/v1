package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"v1/internal/push"
	"v1/internal/store"
)

const (
	testP256dh = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	testAuth   = "BTBZMqHH6r4Tts7J_aSIgg"
)

func pushPost(t *testing.T, s *Server, cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Cookie", cookieHeader(cookie))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

// A subscription is a URL the server will POST to on every turn, so it is
// validated before being stored.
func TestPushSubscribeValidation(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	valid := `{"endpoint":"https://push.example.com/send/abc","p256dh":"` + testP256dh + `","auth":"` + testAuth + `"}`

	cases := []struct {
		name string
		body string
	}{
		{"missing endpoint", `{"p256dh":"` + testP256dh + `","auth":"` + testAuth + `"}`},
		{"missing keys", `{"endpoint":"https://push.example.com/send/abc"}`},
		{"plain http endpoint", `{"endpoint":"http://push.example.com/send/abc","p256dh":"` + testP256dh + `","auth":"` + testAuth + `"}`},
		{"unencryptable keys", `{"endpoint":"https://push.example.com/send/abc","p256dh":"AAAA","auth":"BBBB"}`},
		{"bad json", `{`},
	}
	for _, tc := range cases {
		if got := pushPost(t, s, adminCookie, "/api/push/subscribe", tc.body).Code; got != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, got)
		}
	}

	if got := pushPost(t, s, adminCookie, "/api/push/subscribe", valid).Code; got != http.StatusNoContent {
		t.Fatalf("valid subscription: status = %d, want 204", got)
	}
	// Re-registering the same device refreshes it rather than duplicating it —
	// the browser re-subscribes on every load.
	if got := pushPost(t, s, adminCookie, "/api/push/subscribe", valid).Code; got != http.StatusNoContent {
		t.Fatalf("re-register: status = %d", got)
	}
	user, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	subs, err := s.st.ListPushSubscriptions(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(subs))
	}

	// A device cannot be removed with an empty endpoint.
	if got := pushPost(t, s, adminCookie, "/api/push/unsubscribe", `{}`).Code; got != http.StatusBadRequest {
		t.Fatalf("unsubscribe without an endpoint: status = %d, want 400", got)
	}
	if got := pushPost(t, s, adminCookie, "/api/push/unsubscribe", `{"endpoint":"https://push.example.com/send/abc"}`).Code; got != http.StatusNoContent {
		t.Fatalf("unsubscribe: status = %d, want 204", got)
	}
	if subs, _ = s.st.ListPushSubscriptions(user.ID); len(subs) != 0 {
		t.Fatalf("subscriptions after unsubscribe = %d, want 0", len(subs))
	}
}

// One account must not be able to delete another's device.
func TestPushUnsubscribeIsScopedToTheUser(t *testing.T) {
	s, adminCookie, aliceCookie := newAuthServer(t)
	valid := `{"endpoint":"https://push.example.com/send/abc","p256dh":"` + testP256dh + `","auth":"` + testAuth + `"}`
	if got := pushPost(t, s, adminCookie, "/api/push/subscribe", valid).Code; got != http.StatusNoContent {
		t.Fatalf("subscribe: status = %d", got)
	}
	if got := pushPost(t, s, aliceCookie, "/api/push/unsubscribe", `{"endpoint":"https://push.example.com/send/abc"}`).Code; got != http.StatusNoContent {
		t.Fatalf("unsubscribe: status = %d", got)
	}
	admin, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if subs, _ := s.st.ListPushSubscriptions(admin.ID); len(subs) != 1 {
		t.Fatalf("another user deleted the subscription: %d remain, want 1", len(subs))
	}
}

// The application server key is generated on first use and must never change,
// or every existing subscription becomes undecryptable.
func TestPushVAPIDKeyIsStable(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	get := func() string {
		req := httptest.NewRequest("GET", "/api/push/vapid", nil)
		req.Header.Set("Cookie", cookieHeader(adminCookie))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		var out struct {
			PublicKey string `json:"publicKey"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.PublicKey
	}
	first := get()
	if first == "" {
		t.Fatal("no public key returned")
	}
	if second := get(); second != first {
		t.Fatalf("the application server key changed: %q then %q", first, second)
	}
}

// A subscription the push service reports as gone can never work again, so it
// must be dropped instead of failing on every future turn.
func TestDeliverPushPrunesGoneSubscriptions(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer gone.Close()
	s, _, _ := newAuthServer(t)
	user, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SavePushSubscription(store.PushSubscription{
		Endpoint: gone.URL + "/send/abc", UserID: user.ID,
		P256dh: testP256dh, Auth: testAuth,
	}); err != nil {
		t.Fatal(err)
	}

	s.deliverPush(user.ID, push.Message{Title: "v1", Body: "hi"})

	if subs, err := s.st.ListPushSubscriptions(user.ID); err != nil {
		t.Fatal(err)
	} else if len(subs) != 0 {
		t.Fatalf("a gone subscription must be deleted, %d remain", len(subs))
	}
}

// A working endpoint must actually receive an encrypted push.
func TestDeliverPushSendsEncryptedBody(t *testing.T) {
	var gotBody []byte
	var gotEncoding string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Content-Encoding")
		gotBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer ok.Close()
	s, _, _ := newAuthServer(t)
	user, err := s.st.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SavePushSubscription(store.PushSubscription{
		Endpoint: ok.URL + "/send/abc", UserID: user.ID,
		P256dh: testP256dh, Auth: testAuth,
	}); err != nil {
		t.Fatal(err)
	}

	s.deliverPush(user.ID, push.Message{Title: "proj", Body: "turn finished", URL: "/project/p1"})

	if gotEncoding != "aes128gcm" {
		t.Fatalf("Content-Encoding = %q", gotEncoding)
	}
	// salt(16) + rs(4) + idlen(1) + keyid(65) + payload + tag(16) + delimiter
	if len(gotBody) < 16+4+1+65+16+1 {
		t.Fatalf("body is %d bytes, too short to be a push record", len(gotBody))
	}
	// The subscription must survive a successful send.
	if subs, _ := s.st.ListPushSubscriptions(user.ID); len(subs) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(subs))
	}
}
