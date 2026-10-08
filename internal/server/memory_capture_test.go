package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"v1/internal/config"
	"v1/internal/embed"
	"v1/internal/store"
)

// captureReply is the extraction reply the fake provider returns: one clean
// fact, one carrying a credential inside <private>, and one that must be
// dropped for being empty once stripped.
const captureReply = `[{"content":"the deploy script lives in scripts/deploy.sh","category":"fact","tags":"deploy","importance":2},` +
	`{"content":"the API key is <private>sk-live-abc</private> and it lives in .env","category":"fact","importance":1},` +
	`{"content":"<private>nothing but a secret</private>"}]`

// Turn-end capture stores what the extraction returns, through the same write
// path as the remember tool — so <private> stripping, category validation and
// duplicate refusal apply to it too. It is a second model call on every turn,
// which makes it the one place a bad prompt or a mis-parsed reply would quietly
// fill the store with restatements of the current task.
func TestCaptureMemoriesStoresExtractedFacts(t *testing.T) {
	var prompts []string
	upstream := captureUpstream(t, &prompts, captureReply)

	s, user := captureServer(t, upstream.URL)
	projectID, sessionID, fromID := captureFixture(t, s, user)

	s.captureMemories(projectID, sessionID, fromID, embed.Config{}, user.ID)

	mems, err := s.st.ListMemories(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 2 {
		t.Fatalf("stored %d memories, want 2: %+v", len(mems), mems)
	}
	for _, m := range mems {
		if strings.Contains(m.Content, "sk-live-abc") {
			t.Fatalf("a credential reached the store: %q", m.Content)
		}
		if strings.Contains(m.Content, "nothing but a secret") {
			t.Fatalf("a fully private fact was stored: %q", m.Content)
		}
	}

	// The prompt must carry this turn's exchange and nothing from the
	// transcript: memories are injected into every later turn, so feeding the
	// transcript back would let the model re-derive its own earlier memories.
	if len(prompts) != 1 {
		t.Fatalf("the extraction made %d calls, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "scripts/deploy.sh") {
		t.Fatalf("the exchange never reached the model: %s", prompts[0])
	}
	if !strings.Contains(prompts[0], "Reply with ONLY a JSON array") {
		t.Fatalf("the extraction instruction is missing: %s", prompts[0])
	}

	// Running again over the same exchange must not double the store: the
	// extraction is free to hand back a fact it already returned.
	s.captureMemories(projectID, sessionID, fromID, embed.Config{}, user.ID)
	again, err := s.st.ListMemories(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("a repeated extraction stored duplicates: %d memories", len(again))
	}
}

// A failed extraction leaves the turn and the store alone. Capture runs after
// the turn has already finished, so there is nothing to report and nothing
// worth losing.
func TestCaptureMemoriesSurvivesAFailedCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	s, user := captureServer(t, upstream.URL)
	projectID, sessionID, fromID := captureFixture(t, s, user)

	s.captureMemories(projectID, sessionID, fromID, embed.Config{}, user.ID)

	if mems, err := s.st.ListMemories(projectID); err != nil || len(mems) != 0 {
		t.Fatalf("a failed extraction stored %d memories (err %v)", len(mems), err)
	}
}

// A reply that is not JSON at all yields nothing rather than a partial guess.
func TestCaptureMemoriesIgnoresAnUnparseableReply(t *testing.T) {
	upstream := captureUpstream(t, nil, "Nothing worth remembering here.")

	s, user := captureServer(t, upstream.URL)
	projectID, sessionID, fromID := captureFixture(t, s, user)

	s.captureMemories(projectID, sessionID, fromID, embed.Config{}, user.ID)

	if mems, err := s.st.ListMemories(projectID); err != nil || len(mems) != 0 {
		t.Fatalf("an unparseable reply stored %d memories (err %v)", len(mems), err)
	}
}

// captureUpstream serves one OpenAI-compatible completion returning content.
// When prompts is non-nil it collects the request bodies.
func captureUpstream(t *testing.T, prompts *[]string, content string) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if prompts != nil {
			*prompts = append(*prompts, string(raw))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` +
			strconv.Quote(content) + `},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

// captureServer builds a server whose effective LLM config points at upstream.
func captureServer(t *testing.T, upstreamURL string) (*Server, *store.User) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(config.Config{DataDir: t.TempDir()}, st)
	u, err := s.auth.CreateUser("admin", "pw", true)
	if err != nil {
		t.Fatal(err)
	}
	for key, v := range map[string]string{
		keyLLMBaseURL: upstreamURL,
		keyLLMAPIKey:  "k",
		keyLLMModel:   "m",
	} {
		if err := s.st.SetUserSetting(u.ID, key, v); err != nil {
			t.Fatal(err)
		}
	}
	return s, u
}

// captureFixture creates a project holding one finished turn, and returns the
// project, its session, and the id of the turn's first user message.
func captureFixture(t *testing.T, s *Server, user *store.User) (string, string, int64) {
	t.Helper()
	p := &store.Project{ID: store.NewID(), Name: "capture", Path: t.TempDir(), OwnerID: user.ID}
	if err := s.st.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	session, err := s.st.EnsureDefaultSession(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.AddMessage(p.ID, session.ID, "user", "where does the deploy script live?", "", "m", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.AddMessage(p.ID, session.ID, "assistant", "It is scripts/deploy.sh, and it restarts the service.", "", "m", "", "", ""); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.st.ListMessages(p.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fromID int64
	for _, m := range msgs {
		if m.Role == "user" {
			fromID = m.ID
			break
		}
	}
	if fromID == 0 {
		t.Fatal("the fixture has no user message")
	}
	return p.ID, session.ID, fromID
}

// The setting is off unless the user turns it on: capture costs a model call
// per turn, so it must never be on by accident.
func TestMemoryAutoCaptureIsOffByDefault(t *testing.T) {
	s, user := captureServer(t, "http://127.0.0.1:1")
	if s.memoryAutoCapture(user.ID) {
		t.Fatal("automatic remembering is on by default")
	}
	if err := s.st.SetUserSetting(user.ID, keyMemoryAutoCapture, "1"); err != nil {
		t.Fatal(err)
	}
	if !s.memoryAutoCapture(user.ID) {
		t.Fatal("the setting did not take effect")
	}
}

// The extraction is told what the project already remembers, so the cheap half
// of deduplication happens before anything is embedded.
func TestCapturePromptListsKnownMemories(t *testing.T) {
	s, user := captureServer(t, "http://127.0.0.1:1")
	projectID, sessionID, fromID := captureFixture(t, s, user)
	if _, err := s.st.AddMemory(projectID, "the user prefers Tailwind over CSS modules", "preference", 2); err != nil {
		t.Fatal(err)
	}
	// A disabled memory is not injected into any prompt, so it must not be
	// treated as already covered either.
	if id, err := s.st.AddMemory(projectID, "an old fact nobody wants", "fact", 1); err != nil {
		t.Fatal(err)
	} else if err := s.st.SetMemoryEnabled(projectID, id, false); err != nil {
		t.Fatal(err)
	}

	var prompts []string
	upstream := captureUpstream(t, &prompts, "[]")
	defer upstream.Close()
	s.st.SetUserSetting(user.ID, keyLLMBaseURL, upstream.URL)

	s.captureMemories(projectID, sessionID, fromID, embed.Config{}, user.ID)

	if len(prompts) != 1 {
		t.Fatalf("the extraction made %d calls, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "prefers Tailwind") {
		t.Fatalf("known memories are missing from the prompt: %s", prompts[0])
	}
	if strings.Contains(prompts[0], "an old fact nobody wants") {
		t.Fatalf("a disabled memory was listed as known: %s", prompts[0])
	}
}
