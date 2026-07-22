package inbound

import (
	"net"
	"net/http"
	"strconv"
	"time"
)

// maxBodyBytes caps the inbound request body. Mattermost outgoing-webhook
// payloads are tiny (text fields capped at 4000 chars) so 64 KiB is a
// generous upper bound that still bounds memory use against malformed
// or hostile callers.
const maxBodyBytes = 64 * 1024

// healthzPath is appended to the configured base path and returns a
// plaintext "ok" with no auth. Bound to loopback as a defense-in-depth
// check so the health probe is never reachable from the public internet
// even when an operator misconfigures the bind host.
const healthzPath = "/healthz"

// NewHandler returns the http.Handler that serves the configured base
// path (POST: webhook receiver) and the suffixed /healthz endpoint
// (GET: liveness probe).
//
// The `now` argument is injected for testability — production callers
// pass time.Now. It is consulted only for replay-window enforcement.
func NewHandler(basePath string, store *TokenStore, disp CommandDispatcher, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	mux := http.NewServeMux()
	mux.Handle(basePath, postHandler(store, disp, now))
	mux.Handle(basePath+healthzPath, healthHandler())
	return mux
}

func postHandler(store *TokenStore, disp CommandDispatcher, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Mattermost always sends application/x-www-form-urlencoded.
		ct := r.Header.Get("Content-Type")
		if ct != "" {
			// Strip any "; charset=..." suffix before comparing.
			if i := indexByte(ct, ';'); i >= 0 {
				ct = ct[:i]
			}
		}
		if ct != "application/x-www-form-urlencoded" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}

		// Cap body size before parsing so a malicious caller can not
		// pin memory by streaming a huge form.
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}

		token := r.PostForm.Get("token")
		channel := r.PostForm.Get("channel_name")
		triggerID := r.PostForm.Get("trigger_id")

		// Channel + token check. We deliberately return the same generic
		// 401 for "no such channel" and "wrong token" so an attacker
		// can not enumerate configured channels by probing tokens.
		expected, ok := store.Get(channel)
		if !ok || !verifyToken([]byte(token), []byte(expected)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Replay protection. Prefer X-Request-Timestamp when set (so
		// reverse-proxies and tests can pin the timestamp), falling back
		// to the trigger_id suffix. If neither yields a parseable
		// timestamp, treat the request as stale rather than letting it
		// through unverified.
		ts, ok := requestTimestamp(r, triggerID)
		if !ok || isStale(ts, now(), DefaultReplayWindow) {
			http.Error(w, "request timeout", http.StatusRequestTimeout)
			return
		}

		cmd := MattermostCommand{
			TeamID:      r.PostForm.Get("team_id"),
			ChannelID:   r.PostForm.Get("channel_id"),
			ChannelName: channel,
			UserID:      r.PostForm.Get("user_id"),
			UserName:    r.PostForm.Get("user_name"),
			Command:     r.PostForm.Get("command"),
			Text:        r.PostForm.Get("text"),
			TriggerWord: r.PostForm.Get("trigger_word"),
			TriggerID:   triggerID,
			ResponseURL: r.PostForm.Get("response_url"),
		}

		if err := disp.Dispatch(r.Context(), cmd); err != nil {
			// Dispatch failures are operator-visible but should not
			// leak internals to the caller. Mattermost retries 5xx
			// responses, which is undesirable for a poison message —
			// return 200 with a no-op JSON body so the user sees a
			// generic failure ack instead.
			respondAck(w)
			return
		}

		respondAck(w)
	})
}

func healthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Defense-in-depth: hide the health endpoint from non-loopback
		// callers so a misconfigured bind host does not turn it into a
		// public reachability probe.
		if !isLoopback(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func respondAck(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

// requestTimestamp returns the request's effective timestamp and a bool
// indicating whether one was successfully recovered. Priority:
//
//  1. X-Request-Timestamp header parsed as unix-millisecond integer.
//  2. trigger_id suffix parsed by parseTriggerTimestamp.
func requestTimestamp(r *http.Request, triggerID string) (time.Time, bool) {
	if hdr := r.Header.Get("X-Request-Timestamp"); hdr != "" {
		if ms, err := strconv.ParseInt(hdr, 10, 64); err == nil {
			return time.UnixMilli(ms), true
		}
	}
	if triggerID != "" {
		if t, err := parseTriggerTimestamp(triggerID); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// isLoopback returns true when the supplied RemoteAddr (host:port form
// from net/http) belongs to a loopback interface. An unparseable
// address is treated as non-loopback so we fail closed.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// httptest sometimes sets RemoteAddr without a port — fall
		// back to treating the entire string as the host.
		host = remoteAddr
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// indexByte is a tiny helper to avoid pulling in strings just for one
// LastIndexByte call. Returns -1 when c is not present in s.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
