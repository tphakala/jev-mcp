package jev_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tphakala/jev-mcp/internal/jev"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func decodeResponse(t *testing.T, name string) jev.Response {
	t.Helper()
	var r jev.Response
	if err := json.Unmarshal(readFixture(t, name), &r); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return r
}

func TestResponseDecodeChoice(t *testing.T) {
	t.Parallel()

	r := decodeResponse(t, "response_choice.json")
	if r.Model != "jev-1.13.0" {
		t.Errorf("Model = %q, want jev-1.13.0", r.Model)
	}
	a, ok := r.Answers["tone"]
	if !ok {
		t.Fatal("answer \"tone\" missing")
	}
	if a.Type != jev.TypeChoice {
		t.Errorf("Type = %q, want choice", a.Type)
	}
	if a.Choice != "frustrated" {
		t.Errorf("Choice = %q, want frustrated", a.Choice)
	}
	if got := a.Probabilities["frustrated"]; got != 0.7 {
		t.Errorf("Probabilities[frustrated] = %v, want 0.7", got)
	}
	if a.Confidence == nil || *a.Confidence != 0.62 {
		t.Errorf("Confidence = %v, want 0.62", a.Confidence)
	}
	if r.Usage.InputTokens != 120 || r.Usage.OutputTokens != 8 {
		t.Errorf("Usage = %+v, want {120 8}", r.Usage)
	}
	if len(a.Raw) == 0 {
		t.Error("Raw was not populated")
	}
}

func TestResponseDecodeScore(t *testing.T) {
	t.Parallel()

	r := decodeResponse(t, "response_score.json")
	a := r.Answers["severity"]
	if a.Type != jev.TypeScore {
		t.Errorf("Type = %q, want score", a.Type)
	}
	if a.Score == nil || *a.Score != 1.43 {
		t.Errorf("Score = %v, want 1.43", a.Score)
	}
	// Probabilities are keyed by level index as string keys.
	if got := a.Probabilities["1"]; got != 0.57 {
		t.Errorf("Probabilities[\"1\"] = %v, want 0.57", got)
	}
	// A string legend value stays a JSON string in Raw form.
	got := legendString(t, a.Legend["1"])
	if got != "Degraded" {
		t.Errorf("Legend[\"1\"] = %q, want Degraded", got)
	}
}

func TestResponseDecodeScoreStructuredLegend(t *testing.T) {
	t.Parallel()

	r := decodeResponse(t, "response_score_structured.json")
	a := r.Answers["severity"]
	// With object criteria the legend value is an object, not a string. It must
	// survive decoding as raw JSON rather than break the decode.
	raw, ok := a.Legend["0"]
	if !ok {
		t.Fatal("Legend[\"0\"] missing")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("structured legend value is not an object: %v", err)
	}
	if _, ok := obj["what"]; !ok {
		t.Errorf("structured legend value = %s, want a \"what\" key", raw)
	}
}

func TestResponseDecodeNoul(t *testing.T) {
	t.Parallel()

	r := decodeResponse(t, "response_noul.json")
	a := r.Answers["returning"]
	if a.Type != jev.TypeNoul {
		t.Errorf("Type = %q, want noul", a.Type)
	}
	if a.Noul == nil || *a.Noul != 0.87 {
		t.Errorf("Noul = %v, want 0.87", a.Noul)
	}
	if a.Confidence != nil {
		t.Errorf("Confidence = %v, want nil for noul", a.Confidence)
	}
}

func TestResponseDecodeOpenRouter(t *testing.T) {
	t.Parallel()

	r := decodeResponse(t, "response_openrouter.json")
	if r.ID != "gen-abc123" {
		t.Errorf("ID = %q, want gen-abc123", r.ID)
	}
	if r.Provider != "TypeSafe" {
		t.Errorf("Provider = %q, want TypeSafe", r.Provider)
	}
	if r.Model != "typesafe/jev-1.13-20260917" {
		t.Errorf("Model = %q, want the dated OpenRouter id", r.Model)
	}
	if r.Usage.Cost == nil || *r.Usage.Cost != 0.00003 {
		t.Errorf("Usage.Cost = %v, want 0.00003", r.Usage.Cost)
	}
}

func TestResponseDecodeUnknownTypeAndFields(t *testing.T) {
	t.Parallel()

	// An unrecognised answer type and unknown fields must not fail the decode,
	// and the verbatim object must survive in Raw so nothing is lost.
	r := decodeResponse(t, "response_unknown.json")
	a := r.Answers["mystery"]
	if a.Type != "quantum" {
		t.Errorf("Type = %q, want quantum passed through verbatim", a.Type)
	}
	if a.Choice != "" || a.Score != nil || a.Noul != nil {
		t.Errorf("typed fields should be zero for an unknown type, got %+v", a)
	}
	if len(a.Raw) == 0 {
		t.Fatal("Raw was not populated for the unknown answer")
	}
	var round map[string]json.RawMessage
	if err := json.Unmarshal(a.Raw, &round); err != nil {
		t.Fatalf("Raw is not valid JSON: %v", err)
	}
	if _, ok := round["weirdness"]; !ok {
		t.Errorf("Raw = %s, want the unknown \"weirdness\" field preserved", a.Raw)
	}
}

func TestResponseRawPreservesKnownAnswer(t *testing.T) {
	t.Parallel()

	// Raw is the verbatim answer object even for a recognised type: it must
	// re-decode to the same tone answer, not just contain a matching token
	// (the option name "frustrated" also appears as a probabilities key).
	r := decodeResponse(t, "response_choice.json")
	var probe struct {
		Type   string `json:"type"`
		Choice string `json:"choice"`
	}
	if err := json.Unmarshal(r.Answers["tone"].Raw, &probe); err != nil {
		t.Fatalf("Raw does not decode as the tone answer: %v", err)
	}
	if probe.Type != "choice" || probe.Choice != "frustrated" {
		t.Errorf("Raw decoded to type=%q choice=%q, want choice/frustrated (raw=%s)", probe.Type, probe.Choice, r.Answers["tone"].Raw)
	}
}

func TestResponseDecodeMalformed(t *testing.T) {
	t.Parallel()

	// A malformed payload must surface as a decode error, not a panic or a
	// silently-zero Response.
	var r jev.Response
	if err := json.Unmarshal([]byte("{"), &r); err == nil {
		t.Fatal("decoding malformed JSON returned nil error, want a decode error")
	}
}

// FuzzResponseUnmarshal pins the decoder's reason to exist: every answer that
// decodes must carry a non-empty, valid-JSON Raw whose own "type" matches the
// typed Answer.Type. A regression in the raw-capture loop (an empty Raw, a Raw
// taken from the wrong answer) fails this even on inputs no fixture covers.
func FuzzResponseUnmarshal(f *testing.F) {
	for _, name := range []string{
		"response_choice.json", "response_score.json", "response_score_structured.json",
		"response_noul.json", "response_openrouter.json", "response_unknown.json",
	} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatalf("seed %s: %v", name, err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var r jev.Response
		if err := json.Unmarshal(data, &r); err != nil {
			return // undecodable input is not this codec's concern
		}
		for name, a := range r.Answers {
			if len(a.Raw) == 0 {
				t.Fatalf("answer %q decoded but Raw is empty", name)
			}
			if !json.Valid(a.Raw) {
				t.Fatalf("answer %q Raw is not valid JSON: %s", name, a.Raw)
			}
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(a.Raw, &probe); err != nil {
				t.Fatalf("answer %q Raw does not re-decode: %v", name, err)
			}
			if probe.Type != string(a.Type) {
				t.Fatalf("answer %q Raw type %q != Answer.Type %q", name, probe.Type, a.Type)
			}
		}
	})
}

// legendString unmarshals a raw legend value that is expected to be a JSON
// string, failing the test otherwise.
func legendString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("legend value %s is not a string: %v", raw, err)
	}
	return s
}
