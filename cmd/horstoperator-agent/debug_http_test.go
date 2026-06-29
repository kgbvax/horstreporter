package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestBodyPreviewRedactsSecrets(t *testing.T) {
	in := []byte(`{"key":"SUPERSECRET123","station_id":"7","fetchfromid":0}`)
	got := bodyPreview(in)
	if strings.Contains(got, "SUPERSECRET123") {
		t.Fatalf("preview leaked the API key: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("preview did not redact the key: %q", got)
	}
	if !strings.Contains(got, `"station_id":"7"`) {
		t.Fatalf("preview dropped non-secret fields: %q", got)
	}
}

func TestBodyPreviewTruncates(t *testing.T) {
	long := []byte(strings.Repeat("a", debugBodyPreview*2))
	got := bodyPreview(long)
	if len([]rune(got)) > debugBodyPreview+len("body=")+1 {
		t.Fatalf("preview not truncated: len=%d", len([]rune(got)))
	}
}

func TestRedactURLStripsSecretQuery(t *testing.T) {
	u, _ := url.Parse("https://api.pota.app/profile?token=abc123&call=DL0HRP")
	got := redactURL(u)
	if strings.Contains(got, "abc123") {
		t.Fatalf("redactURL leaked token: %q", got)
	}
	if !strings.Contains(got, "call=DL0HRP") {
		t.Fatalf("redactURL dropped non-secret param: %q", got)
	}
}
