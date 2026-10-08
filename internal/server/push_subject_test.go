package server

import (
	"strings"
	"testing"
)

// TestPushSubjectIsRoutable guards the VAPID contact claim.
//
// Apple validates it and answers a non-routable contact with 403
// {"reason":"BadJwtToken"}; no other push service does. The mistake is therefore
// invisible to every test that does not actually deliver, and it presents as
// iOS silently receiving nothing while the server logs a send that "happened".
func TestPushSubjectIsRoutable(t *testing.T) {
	if rest, ok := strings.CutPrefix(pushSubject, "mailto:"); ok {
		at := strings.LastIndex(rest, "@")
		if at < 0 {
			t.Fatalf("pushSubject %q is not a mailto URL", pushSubject)
		}
		domain := rest[at+1:]
		if !strings.Contains(domain, ".") {
			t.Fatalf("pushSubject %q has no routable domain", pushSubject)
		}
		if domain == "localhost" || strings.HasSuffix(domain, ".local") {
			t.Fatalf("pushSubject %q is not routable; Apple answers 403 BadJwtToken", pushSubject)
		}
		return
	}
	if !strings.HasPrefix(pushSubject, "https://") {
		t.Fatalf("pushSubject %q must be a mailto: or https: URL", pushSubject)
	}
}
