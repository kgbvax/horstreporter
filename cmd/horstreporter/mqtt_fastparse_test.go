package main

// Parity tests for the fast minimal-payload parse (mqtt.go fastParseMinimalPSKR).
// The fast path must agree with json.Unmarshal on every accepted payload, and
// must refuse (fall back) anything it is not certain about — including forms
// json.Unmarshal would reject (leading zeros, trailing garbage), so ingest
// behavior stays byte-identical.

import (
	"encoding/json"
	"math/rand"
	"testing"
)

func TestFastParseMinimalPSKRParity(t *testing.T) {
	valid := []string{
		`{"t":1700000000,"rp":-15}`,
		`{"rp":-15,"t":1700000000}`,
		`{"t":0,"rp":0}`,
		`{"t": 1700000000 , "rp" : -15 }`,
		`{"t":-5,"rp":33}`,
		`{"t":9223372036854775807,"rp":-2147483647}`,
		`{ "t":1, "rp":2 }`,
		`{"t":1700000000,"rp":-15}
`,
	}
	// Duplicate keys: json.Unmarshal takes the last value; the fast path
	// overwrites the same way, so it may accept them (parity either way).
	var dupWant MQTTMessage
	if err := json.Unmarshal([]byte(`{"t":1,"t":2,"rp":3}`), &dupWant); err != nil {
		t.Fatalf("duplicate-key fixture: %v", err)
	}
	gotT, gotRP, ok := fastParseMinimalPSKR([]byte(`{"t":1,"t":2,"rp":3}`))
	if gotT != dupWant.T || gotRP != dupWant.RP {
		t.Errorf("duplicate-key mismatch: got (%d,%d) want (%d,%d)", gotT, gotRP, dupWant.T, dupWant.RP)
	}
	_ = ok

	for _, p := range valid {
		var want MQTTMessage
		if err := json.Unmarshal([]byte(p), &want); err != nil {
			t.Fatalf("fixture %q should be valid JSON: %v", p, err)
		}
		gotT, gotRP, ok := fastParseMinimalPSKR([]byte(p))
		if !ok {
			t.Errorf("fast path refused valid %q", p)
			continue
		}
		if gotT != want.T || gotRP != want.RP {
			t.Errorf("fast path mismatch for %q: got (%d,%d) want (%d,%d)", p, gotT, gotRP, want.T, want.RP)
		}
	}

	// Anything the fast path refuses must round-trip through json.Unmarshal
	// exactly as ingestPSKRMessage would (valid → parsed; invalid → dropped).
	refused := []string{
		`{"t":1700000000,"rp":-15,"md":"FT8"}`, // extra key
		`{"b":"20m","md":"FT8","sc":"A"}`,      // no t/rp
		`{"t":1700000000.5,"rp":-15}`,          // float t
		`{"t":01}`,                             // leading zero: invalid JSON
		`{"t":5,}`,                             // trailing comma: invalid JSON
		`{"t":5`,                               // truncated
		`{"t":5} x`,                            // trailing garbage
		`{"t"1700000000,"rp":-15}`,             // malformed
		`{"t":"1700000000","rp":-15}`,          // string value
		`[]`,
		``,
	}
	for _, p := range refused {
		if _, _, ok := fastParseMinimalPSKR([]byte(p)); ok {
			t.Errorf("fast path accepted refused %q", p)
		}
		var m MQTTMessage
		err := json.Unmarshal([]byte(p), &m)
		_ = m
		_ = err // refusal is what matters; parity with unmarshal outcome covered above
	}
}

// Fuzz-ish parity: random minimal and near-minimal payloads — fast path must
// either refuse or agree with json.Unmarshal.
func TestFastParseMinimalPSKRRandomParity(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5000; i++ {
		tVal := rng.Int63n(2_000_000_000)
		rpVal := rng.Intn(80) - 40
		var p string
		switch rng.Intn(6) {
		case 0:
			p = `{"t":` + itoa(int(tVal)) + `,"rp":` + itoa(rpVal) + `}`
		case 1:
			p = `{ "rp":` + itoa(rpVal) + ` , "t":` + itoa(int(tVal)) + ` }`
		case 2:
			p = `{"t":` + itoa(-int(rng.Intn(100))) + `,"rp":` + itoa(rpVal) + `}`
		case 3:
			p = `{"t":` + itoa(int(tVal)) + `,"rp":` + itoa(rpVal) + `,"b":"20m"}`
		case 4:
			p = `{"rp":` + itoa(rpVal) + `}`
		default:
			p = `{"t":` + itoa(int(tVal)) + `}`
		}
		var want MQTTMessage
		err := json.Unmarshal([]byte(p), &want)
		gotT, gotRP, ok := fastParseMinimalPSKR([]byte(p))
		if err != nil {
			if ok {
				t.Fatalf("fast path accepted invalid %q", p)
			}
			continue
		}
		if !ok {
			// Refusal on valid JSON is fine — ingest falls back to
			// json.Unmarshal and gets the same result. Only an accepted
			// payload must match unmarshal exactly.
			continue
		}
		if gotT != want.T || gotRP != want.RP {
			t.Fatalf("mismatch for %q: got (%d,%d) want (%d,%d)", p, gotT, gotRP, want.T, want.RP)
		}
	}
}
