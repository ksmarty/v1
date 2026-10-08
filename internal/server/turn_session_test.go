package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"v1/internal/config"
	"v1/internal/store"
)

// sseChunks writes OpenAI-style streamed chunks, which is the only shape the
// client parses: it always requests `stream: true`.
func sseChunks(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// TestTurnToolsKnowTheirSession drives a real chat turn whose model calls
// set_session_name and checks the rename landed.
//
// It covers the wiring both harnesses share: PrepareExecutor is what hands the
// tool executor its session id, and a turn that never got one would answer
// "no active chat session" while looking, from the transcript, like a tool that
// simply did nothing. Nothing else observes the executor the handler builds.
func TestTurnToolsKnowTheirSession(t *testing.T) {
	var calls atomic.Int32
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First round: ask for the rename. Second: answer, ending the turn.
		if calls.Add(1) == 1 {
			sseChunks(w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"set_session_name","arguments":"{\"name\":\"Renamed by the agent\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			)
			return
		}
		sseChunks(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer llmSrv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Config{
		DataDir:      t.TempDir(),
		AuthDisabled: true,
		OpenAIBase:   llmSrv.URL,
		OpenAIKey:    "k",
		Model:        "m",
	}, st)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects",
		strings.NewReader(`{"name":"tool session test"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	pid := created.ID
	session, err := st.EnsureDefaultSession(pid)
	if err != nil {
		t.Fatal(err)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects/"+pid+"/chat",
		strings.NewReader(`{"message":"name this session","sessionId":"`+session.ID+`"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rr.Code, rr.Body.String())
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("the model was called %d time(s), want the tool round then a final answer", n)
	}

	sessions, err := st.ListSessions(pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range sessions {
		if cs.ID != session.ID {
			continue
		}
		if cs.Name != "Renamed by the agent" {
			t.Fatalf("session name = %q, want the tool's rename — the turn's executor had no session id", cs.Name)
		}
		return
	}
	t.Fatalf("session %s is missing from the project", session.ID)
}

// TestTurnReportsBackgroundCompletion drives a turn whose model starts a
// detached command, and checks the stream reports that command stopping.
//
// background_started puts the job in the UI's running badge. With no matching
// done event the badge counted every job ever started, while the tasks modal —
// which asks the server what is actually running — correctly showed none, so
// the two disagreed and the modal looked broken.
func TestTurnReportsBackgroundCompletion(t *testing.T) {
	var calls atomic.Int32
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			sseChunks(w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"run_command_background","arguments":"{\"command\":\"true\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			)
			return
		}
		// Give the detached command time to finish so its completion is emitted
		// into this still-open stream rather than after it closes.
		time.Sleep(200 * time.Millisecond)
		sseChunks(w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer llmSrv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(config.Config{
		DataDir:      t.TempDir(),
		AuthDisabled: true,
		OpenAIBase:   llmSrv.URL,
		OpenAIKey:    "k",
		Model:        "m",
	}, st)
	// Approve tool calls without prompting: the permission gate would otherwise
	// block on a user decision no test can give.
	if err := st.SetSetting("permission_mode", "yolo"); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects",
		strings.NewReader(`{"name":"background test"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/projects/"+created.ID+"/chat",
		strings.NewReader(`{"message":"start it"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"background_started"`) {
		t.Fatalf("the stream never reported the job starting:\n%s", body)
	}
	if !strings.Contains(body, `"background_done"`) {
		t.Fatalf("the stream never reported the job finishing:\n%s", body)
	}
}
