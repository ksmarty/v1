package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A file is uploaded when it is attached, so the turn request carries its id
// instead of its bytes. That is what keeps a large attachment from being lost
// halfway through a chat request.
func TestUploadsRoundTripThroughChat(t *testing.T) {
	s, adminCookie, _ := newAuthServer(t)
	pid := newTestProject(t, s, adminCookie, "uploads")

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Cookie", cookieHeader(adminCookie))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		return rr
	}

	up := post("/api/projects/"+pid+"/uploads?sessionId=s1",
		`{"name":"notes.txt","mime":"text/plain","kind":"text","content":"hello"}`)
	if up.Code != http.StatusOK {
		t.Fatalf("upload = %d, body %s", up.Code, up.Body.String())
	}
	var uploaded struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(up.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.ID == "" || uploaded.Name != "notes.txt" || uploaded.Size != 5 {
		t.Fatalf("upload response = %+v", uploaded)
	}

	// An id that is gone (the chip was removed, or another project's file) is
	// refused before a turn can start with a hole in it.
	bad := post("/api/projects/"+pid+"/chat", `{"message":"hi","uploadIds":["missing"]}`)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "no longer available") {
		t.Fatalf("stale upload = %d, body %s", bad.Code, bad.Body.String())
	}

	// The real id resolves: this request then stops at the provider key, which
	// proves it got past attachment resolution.
	ok := post("/api/projects/"+pid+"/chat", `{"message":"hi","uploadIds":["`+uploaded.ID+`"]}`)
	if ok.Code == http.StatusBadRequest && strings.Contains(ok.Body.String(), "no longer available") {
		t.Fatalf("upload did not resolve: %s", ok.Body.String())
	}

	// Removing a chip drops the file.
	if del := post("/api/projects/"+pid+"/uploads/delete", `{"ids":["`+uploaded.ID+`"]}`); del.Code != http.StatusOK {
		t.Fatalf("delete uploads = %d, body %s", del.Code, del.Body.String())
	}
	rows, err := s.st.ListUploads(pid, []string{uploaded.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("upload survived delete: %+v", rows)
	}
}
