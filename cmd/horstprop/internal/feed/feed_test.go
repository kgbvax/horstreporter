package feed

import (
	"strings"
	"testing"
	"time"

	"horstreporter/internal/propcontract"
)

var fixedNow = time.Date(2026, 6, 24, 13, 22, 5, 0, time.UTC)

func TestMapSpot(t *testing.T) {
	// mqtt spot maps; dxcluster dropped; missing band dropped.
	mqtt := streamSpot{SNR: 14, AgeSeconds: 60, Locator: "oh29", ReporterLocator: "jo31", SourceType: "mqtt", Band: "20m"}
	pr, ok := mapSpot(mqtt, fixedNow)
	if !ok {
		t.Fatal("mqtt spot should map")
	}
	if pr.TxLocator != "OH29" || pr.RxLocator != "JO31" || pr.SNRDb != 14 || pr.Band != "20m" || pr.Source != "mqtt" {
		t.Errorf("unexpected mapping: %+v", pr)
	}
	if want := fixedNow.Add(-60 * time.Second); !pr.ObservedAt.Equal(want) {
		t.Errorf("ObservedAt=%v want %v", pr.ObservedAt, want)
	}

	if _, ok := mapSpot(streamSpot{SourceType: "dxcluster", Band: "20m", Locator: "oh29"}, fixedNow); ok {
		t.Error("dxcluster spot must be dropped")
	}
	if _, ok := mapSpot(streamSpot{SourceType: "mqtt", Locator: "oh29"}, fixedNow); ok {
		t.Error("spot with no band must be dropped")
	}
}

func TestConsumeSSE(t *testing.T) {
	stream := strings.Join([]string{
		"data: {\"snr\":14,\"ageSeconds\":60,\"locator\":\"OH29\",\"reporterLocator\":\"JO31\",\"sourceType\":\"mqtt\",\"band\":\"20m\"}",
		"",
		"data: {\"snr\":3,\"locator\":\"FM18\",\"sourceType\":\"dxcluster\",\"band\":\"40m\"}", // dropped
		"",
		"event: history_end",
		"data: {}",
		"",
		"data: {\"snr\":-5,\"ageSeconds\":10,\"locator\":\"PM95\",\"reporterLocator\":\"JO31\",\"sourceType\":\"mqtt\",\"band\":\"15m\"}",
		"",
	}, "\n")

	var got []propcontract.PropReport
	s := &Source{Now: func() time.Time { return fixedNow }}
	if err := s.consume(strings.NewReader(stream), func(pr propcontract.PropReport) { got = append(got, pr) }); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d reports, want 2 (dxcluster + sentinel dropped): %+v", len(got), got)
	}
	if got[0].Band != "20m" || got[1].Band != "15m" {
		t.Errorf("unexpected bands: %q, %q", got[0].Band, got[1].Band)
	}
}
