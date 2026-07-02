package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// debugBodyPreview caps how many bytes of each request/response body are logged.
// Small enough to stay readable and to avoid dumping a whole ADIF log pull.
const debugBodyPreview = 512

// secretJSONRe redacts the Wavelog read key (and similar) when it rides in a JSON
// body, e.g. {"key":"abc..."} for private_lookup / get_contacts_adif.
var secretJSONRe = regexp.MustCompile(`(?i)("(?:key|api_key|apikey|password|token)"\s*:\s*")[^"]*(")`)

// secretQueryKeys are URL query params whose values must never be logged.
var secretQueryKeys = map[string]bool{"key": true, "api_key": true, "apikey": true, "token": true, "password": true}

// loggingRoundTripper logs every outbound HTTP request/response (method, URL,
// status, duration, and a short redacted body preview). It wraps the process
// default transport, so it captures the agent's Wavelog/backend/rig clients, the
// in-process award sources, AND the backend reverse proxy — all of which use the
// default transport. Secrets are redacted; this is the HTTP half of the Settings
// "Debug logging" switch (the PSTrotator UDP half reuses the existing UDP logger).
type loggingRoundTripper struct {
	base http.RoundTripper
}

// joinedBody re-presents a partially-peeked body as a single ReadCloser without
// buffering the whole thing, so large bodies still stream to the caller.
type joinedBody struct {
	io.Reader
	c io.Closer
}

func (j *joinedBody) Close() error { return j.c.Close() }

func (t *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	reqNote := ""
	if req.Body != nil {
		peek, _ := io.ReadAll(io.LimitReader(req.Body, debugBodyPreview))
		rest := req.Body
		req.Body = &joinedBody{Reader: io.MultiReader(bytes.NewReader(peek), rest), c: rest}
		reqNote = bodyPreview(peek)
	}
	log.Printf("[DEBUG] ext http \u2192 %s %s%s", req.Method, redactURL(req.URL), sp(reqNote))

	resp, err := t.base.RoundTrip(req)
	dur := time.Since(start).Round(time.Millisecond)
	if err != nil {
		log.Printf("[DEBUG] ext http \u2717 %s %s (%s): %v", req.Method, redactURL(req.URL), dur, err)
		return resp, err
	}

	peek, _ := io.ReadAll(io.LimitReader(resp.Body, debugBodyPreview))
	rest := resp.Body
	resp.Body = &joinedBody{Reader: io.MultiReader(bytes.NewReader(peek), rest), c: rest}
	log.Printf("[DEBUG] ext http \u2190 %d %s %s (%s)%s", resp.StatusCode, req.Method, redactURL(req.URL), dur, sp(bodyPreview(peek)))
	return resp, nil
}

var enableExternalDebugLoggingOnce sync.Once

// externalDebugEnabled mirrors the Settings "Debug logging" switch at package
// scope so components without a serviceConfig handle (e.g. the WaveLogGate WS
// client) can gate verbose logs. Set once at startup; read-only thereafter.
var externalDebugEnabled bool

func debugExternalEnabled() bool { return externalDebugEnabled }

// enableExternalDebugLogging swaps the process default transport for the logging
// one. Idempotent. Clients with a nil Transport resolve http.DefaultTransport at
// request time, so doing this before any request is issued covers them all.
func enableExternalDebugLogging() {
	enableExternalDebugLoggingOnce.Do(func() {
		externalDebugEnabled = true
		base := http.DefaultTransport
		if base == nil {
			base = &http.Transport{}
		}
		http.DefaultTransport = &loggingRoundTripper{base: base}
		log.Printf("[INFO] debug logging enabled: external HTTP + PSTrotator UDP traffic logged (secrets redacted)")
	})
}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return u.String()
	}
	q := u.Query()
	for k := range q {
		if secretQueryKeys[strings.ToLower(k)] {
			q.Set(k, "REDACTED")
		}
	}
	c := *u
	c.RawQuery = q.Encode()
	return c.String()
}

// bodyPreview returns a single-line, secret-redacted, truncated view of a body.
func bodyPreview(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := secretJSONRe.ReplaceAllString(string(b), `${1}REDACTED${2}`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > debugBodyPreview {
		s = s[:debugBodyPreview] + "\u2026"
	}
	return "body=" + s
}

func sp(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
}
