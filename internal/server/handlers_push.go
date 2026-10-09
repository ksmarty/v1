package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"v1/internal/push"
	"v1/internal/sanitize"
	"v1/internal/store"
)

// keyVAPIDPrivate is the per-user Web Push application server key. It is
// generated on first use rather than configured, so turning notifications on
// needs no deployment step and no extra environment variable.
const keyVAPIDPrivate = "push_vapid_private"

// pushSubject is the contact address handed to the push services, which
// RFC 8292 requires. Nothing is ever sent to it.
//
// Apple validates this claim and refuses a contact it considers unroutable:
// `mailto:v1@localhost` is answered with 403 {"reason":"BadJwtToken"}, as is any
// `.local` address, while a routable mailto or https URL is accepted. Because
// only Apple is that strict, the rejection showed up as iOS going silent while
// every other push service still delivered. example.com is reserved by RFC 2606,
// so this can never reach a real inbox.
const pushSubject = "https://example.com"

// maxUserAgent caps the stored device label.
const maxUserAgent = 200

// vapidKeys returns the user's VAPID key pair, creating it on first use.
func (s *Server) vapidKeys(userID string) (*push.VAPIDKeys, error) {
	if raw, ok, err := s.st.GetUserSetting(userID, keyVAPIDPrivate); err != nil {
		return nil, err
	} else if ok && raw != "" {
		return push.ParseVAPIDKeys(raw)
	}
	keys, err := push.GenerateVAPIDKeys()
	if err != nil {
		return nil, err
	}
	if err := s.st.SetUserSetting(userID, keyVAPIDPrivate, keys.PrivateScalar()); err != nil {
		return nil, err
	}
	return keys, nil
}

// deliverPush sends msg to every device the user has registered.
//
// It is synchronous so it can be tested directly; callers that must not block
// use notifyPush. Delivery failures are logged rather than returned: a chat
// turn must never fail because a phone could not be reached. A subscription the
// push service reports as gone is deleted, since it can never work again —
// which is also how a device that uninstalled the PWA gets cleaned up.
func (s *Server) deliverPush(userID string, msg push.Message) {
	subs, err := s.st.ListPushSubscriptions(userID)
	if err != nil {
		log.Printf("push: list subscriptions: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	keys, err := s.vapidKeys(userID)
	if err != nil {
		log.Printf("push: vapid keys: %v", err)
		return
	}
	client := push.NewClient(keys, pushSubject)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, sub := range subs {
		err := client.Send(ctx, push.Subscription{
			Endpoint: sub.Endpoint,
			P256dh:   sub.P256dh,
			Auth:     sub.Auth,
		}, msg)
		switch {
		case err == nil:
		case errors.Is(err, push.ErrGone):
			if err := s.st.DeletePushSubscription(userID, sub.Endpoint); err != nil {
				log.Printf("push: delete dead subscription: %v", err)
			}
		default:
			log.Printf("push: send: %v", err)
		}
	}
}

// notifyPush delivers msg in the background. Unlike the page's own
// showNotification, this reaches the device with the app closed — which is the
// only way a notification arrives after iOS has suspended the PWA.
func (s *Server) notifyPush(userID string, msg push.Message) {
	if userID == "" {
		return
	}
	go s.deliverPush(userID, msg)
}

// handlePushVAPID returns the application server public key the browser must
// subscribe with, creating the key pair on first request.
func (s *Server) handlePushVAPID(w http.ResponseWriter, r *http.Request) {
	keys, err := s.vapidKeys(s.currentUser(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not prepare push keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": keys.PublicKey()})
}

// handlePushSubscribe stores a browser push subscription.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	var body struct {
		Endpoint  string `json:"endpoint"`
		P256dh    string `json:"p256dh"`
		Auth      string `json:"auth"`
		UserAgent string `json:"userAgent"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Endpoint == "" || body.P256dh == "" || body.Auth == "" {
		writeError(w, http.StatusBadRequest, "endpoint, p256dh and auth are required")
		return
	}
	// The endpoint is a URL we will POST to on every turn, so it is constrained
	// to https and bounded in length.
	if len(body.Endpoint) > 2000 || !strings.HasPrefix(body.Endpoint, "https://") {
		writeError(w, http.StatusBadRequest, "endpoint must be an https URL")
		return
	}
	// Refuse a subscription we could never encrypt for, so a broken client does
	// not sit in the table failing on every turn.
	if _, err := push.Encrypt(push.Subscription{
		Endpoint: body.Endpoint, P256dh: body.P256dh, Auth: body.Auth,
	}, []byte("{}")); err != nil {
		writeError(w, http.StatusBadRequest, "invalid subscription key material")
		return
	}
	ua := body.UserAgent
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	err := s.st.SavePushSubscription(store.PushSubscription{
		Endpoint:  body.Endpoint,
		UserID:    userID,
		P256dh:    body.P256dh,
		Auth:      body.Auth,
		UserAgent: sanitize.Text(ua),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not store the subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePushUnsubscribe forgets one of the caller's devices.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	if err := s.st.DeletePushSubscription(s.currentUser(r).ID, body.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, "could not remove the subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePushTest sends a notification to the caller's own devices, so the
// Settings toggle can be verified without waiting for a turn to finish.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	userID := s.currentUser(r).ID
	subs, err := s.st.ListPushSubscriptions(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read subscriptions")
		return
	}
	if len(subs) == 0 {
		writeError(w, http.StatusBadRequest, "no device is subscribed")
		return
	}
	s.deliverPush(userID, push.Message{
		Title: "v1",
		Body:  "Test notification",
		URL:   "/",
		Tag:   "v1-test",
	})
	writeJSON(w, http.StatusOK, map[string]int{"devices": len(subs)})
}

// sessionName names a session for a notification body, empty when it cannot be
// read.
func (s *Server) sessionName(projectID, sessionID string) string {
	sessions, err := s.st.ListSessions(projectID)
	if err != nil {
		return ""
	}
	for _, sess := range sessions {
		if sess.ID == sessionID {
			return sess.Name
		}
	}
	return ""
}

// notifyTurnPush delivers a turn result to the user's devices. This is the only
// notification path that survives iOS suspending the app, where the page's own
// showNotification can never run.
func (s *Server) notifyTurnPush(userID, projectID, sessionID, projectName string, turnErr error) {
	if userID == "" {
		return
	}
	// Nothing to do without a registered device, and this keeps the message
	// query off the hot path of every turn.
	if subs, err := s.st.ListPushSubscriptions(userID); err != nil || len(subs) == 0 {
		return
	}
	link := "/project/" + url.PathEscape(projectID) + "?session=" + url.QueryEscape(sessionID)
	title := projectName
	if title == "" {
		title = "v1"
	}
	// The notification names the turn — project, session, outcome — and nothing
	// else. The reply itself used to be the body, which made it a wall of text
	// truncated mid-sentence that told you nothing you could not read by opening
	// the notification.
	outcome := "finished"
	if turnErr != nil {
		outcome = "failed"
	}
	body := "Turn " + outcome
	if name := s.sessionName(projectID, sessionID); name != "" {
		body = name + " · " + outcome
	}
	s.notifyPush(userID, push.Message{Title: title, Body: body, URL: link, Tag: "v1-turn-" + sessionID})
}
