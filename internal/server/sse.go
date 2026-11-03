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
