package server

import "time"

// sseHeartbeat is how often a chat stream writes an SSE comment.
//
// A turn can sit inside one tool call for minutes, and a stream that is silent
// because nothing is happening looks exactly like a stream whose connection
// died — a restarted server, a dropped network, a proxy that timed the socket
// out. The chat then stayed "running" with no output until the page was
// reloaded. A comment every sseHeartbeat gives the client something to measure
// silence against: the browser abandons a stream that has heard nothing for a
// few beats and recovers from the transcript.
const sseHeartbeat = 15 * time.Second

// sseWriteDeadline bounds a single SSE write. A client that stops reading but
// keeps the socket open (a backgrounded tab whose app was suspended, a
// death-by-NAT connection that never RSTs) makes fmt.Fprintf block once the
// kernel send buffer is full — and emit() runs inside the agent's turn loop,
// so a blocked write froze the whole run while chatStatus still reported it
// as running. The run must not die with the socket: every write gets a
// deadline, the first failure flips a flag that stops further net writes,
// and the hub keeps publishing so a returning client converges over the
// watch/status path.
const sseWriteDeadline = 10 * time.Second
