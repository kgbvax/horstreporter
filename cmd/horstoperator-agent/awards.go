package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"horstreporter/internal/awardcontract"
)

// awardsClient is a thin HTTP client for the horstawards /v1/wanted endpoint. The
// agent resolves a spot's attributes via Wavelog, then asks horstawards which
// award slots those attributes would fill (the operator's progress). Shape mirrors
// wavelogClient: a tiny, dependency-free proxy with a short timeout.
type awardsClient struct {
	endpoint string // <base>/v1/wanted
	http     *http.Client
}

func newAwardsClient(base string) *awardsClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	return &awardsClient{
		endpoint: base + "/v1/wanted",
		http:     &http.Client{Timeout: 6 * time.Second},
	}
}

// Wanted evaluates a batch of resolved spots against the operator's award index.
func (c *awardsClient) Wanted(ctx context.Context, spots []awardcontract.WantedSpot) (*awardcontract.WantedResponse, error) {
	payload, _ := json.Marshal(awardcontract.WantedRequest{PermitLookup: true, Spots: spots})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("horstawards request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("horstawards returned HTTP %d", resp.StatusCode)
	}
	var out awardcontract.WantedResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("horstawards response parse failed: %w", err)
	}
	return &out, nil
}
