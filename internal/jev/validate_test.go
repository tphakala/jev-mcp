package jev_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/tphakala/jev-mcp/internal/jev"
)

const (
	okState = `"a short support message"`
	okInstr = `"decide the tone"`
)

func j(s string) json.RawMessage { return json.RawMessage(s) }

func oneQ(state, name string, q jev.Question) jev.Request {
	return jev.Request{Model: "jev-latest", State: j(state), Questions: map[string]jev.Question{name: q}}
}

func noulQ() jev.Question {
	return jev.Question{Type: jev.TypeNoul, Instructions: j(okInstr)}
}

func manyQuestions(n int) jev.Request {
	qs := make(map[string]jev.Question, n)
	for i := range n {
		qs["q"+strconv.Itoa(i)] = noulQ()
	}
	return jev.Request{Model: "m", State: j(okState), Questions: qs}
}

func choiceWithOptions(n int) jev.Request {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"opt`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":null`)
	}
	b.WriteByte('}')
	return oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(b.String())})
}

func scoreWithLevels(n int) jev.Request {
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"level`)
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return oneQ(okState, "q", jev.Question{Type: jev.TypeScore, Instructions: j(okInstr), Criteria: j(b.String())})
}

func oversizedState() jev.Request {
	big := `"` + strings.Repeat("a", jev.MaxStateBytes) + `"`
	return oneQ(big, "q", noulQ())
}

// maxState builds a request whose state is exactly MaxStateBytes bytes (a valid
// JSON string), so the size guard's upper boundary has a valid case.
func maxState() jev.Request {
	s := `"` + strings.Repeat("a", jev.MaxStateBytes-2) + `"`
	return oneQ(s, "q", noulQ())
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     jev.Request
		wantErr bool
		substr  string
	}{
		// state
		{name: "state missing", req: jev.Request{Questions: map[string]jev.Question{"q": noulQ()}}, wantErr: true, substr: "state is required"},
		{name: "state null", req: oneQ("null", "q", noulQ()), wantErr: true, substr: "state must not be null"},
		{name: "state oversized", req: oversizedState(), wantErr: true, substr: "state is"},
		{name: "state malformed json", req: oneQ(`{"a":`, "q", noulQ()), wantErr: true, substr: "state must be valid JSON"},

		// question count
		{name: "no questions", req: jev.Request{State: j(okState)}, wantErr: true, substr: "got 0"},
		{name: "too many questions", req: manyQuestions(jev.MaxQuestions + 1), wantErr: true, substr: "got 65"},

		// question name
		{name: "empty name", req: oneQ(okState, "", noulQ()), wantErr: true, substr: "a question name must not be empty"},
		{name: "name too long", req: oneQ(okState, strings.Repeat("n", jev.MaxQuestionNameBytes+1), noulQ()), wantErr: true, substr: "over the 128 limit"},
		{name: "name control char", req: oneQ(okState, "bad\x01name", noulQ()), wantErr: true, substr: "control characters"},

		// instructions
		{name: "instructions missing", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul}), wantErr: true, substr: "instructions are required"},
		{name: "instructions blank string", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`"   "`)}), wantErr: true, substr: "must not be blank"},
		{name: "instructions empty object", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`{}`)}), wantErr: true, substr: "object must not be empty"},
		{name: "instructions empty array", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`[]`)}), wantErr: true, substr: "array must not be empty"},
		{name: "instructions number", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`42`)}), wantErr: true, substr: "string, object, or array"},
		{name: "instructions null", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`null`)}), wantErr: true, substr: "string, object, or array"},
		{name: "instructions whitespace only", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j("   ")}), wantErr: true, substr: "string, object, or array"},
		{name: "instructions malformed string", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`"unterminated`)}), wantErr: true, substr: "instructions string is malformed"},
		{name: "instructions malformed object", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`{`)}), wantErr: true, substr: "instructions object is malformed"},
		{name: "instructions malformed array", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`[`)}), wantErr: true, substr: "instructions array is malformed"},

		// type
		{name: "unknown type", req: oneQ(okState, "q", jev.Question{Type: "bogus", Instructions: j(okInstr)}), wantErr: true, substr: "unknown type"},

		// choice
		{name: "choice no criteria", req: oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr)}), wantErr: true, substr: "choice requires criteria"},
		{name: "choice criteria not object", req: oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`["a","b"]`)}), wantErr: true, substr: "choice criteria must be an object"},
		{name: "choice too few options", req: choiceWithOptions(1), wantErr: true, substr: "got 1"},
		{name: "choice too many options", req: choiceWithOptions(jev.MaxChoiceOptions + 1), wantErr: true, substr: "got 256"},
		{name: "choice empty option name", req: oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`{"":"x","b":"y"}`)}), wantErr: true, substr: "option names must not be empty"},
		{name: "choice whitespace option name", req: oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`{"   ":"x","b":"y"}`)}), wantErr: true, substr: "option names must not be empty"},

		// score
		{name: "score no criteria", req: oneQ(okState, "q", jev.Question{Type: jev.TypeScore, Instructions: j(okInstr)}), wantErr: true, substr: "score requires criteria"},
		{name: "score criteria not array", req: oneQ(okState, "q", jev.Question{Type: jev.TypeScore, Instructions: j(okInstr), Criteria: j(`{"0":"low"}`)}), wantErr: true, substr: "must be an ordered array"},
		{name: "score too few levels", req: scoreWithLevels(1), wantErr: true, substr: "got 1"},
		{name: "score too many levels", req: scoreWithLevels(jev.MaxScoreLevels + 1), wantErr: true, substr: "got 11"},
		{name: "score null level", req: oneQ(okState, "q", jev.Question{Type: jev.TypeScore, Instructions: j(okInstr), Criteria: j(`["low",null,"high"]`)}), wantErr: true, substr: "score level 1 must not be null"},

		// noul
		{name: "noul bad key", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(okInstr), Criteria: j(`{"yes":"x"}`)}), wantErr: true, substr: "must be true or false"},
		{name: "noul criteria not object", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(okInstr), Criteria: j(`["true"]`)}), wantErr: true, substr: "object with true and false"},

		// happy paths
		{name: "valid choice", req: oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`{"calm":"relaxed","frustrated":"annoyed"}`)})},
		{name: "valid choice null values", req: oneQ(okState, "q", jev.Question{Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`{"calm":null,"angry":null}`)})},
		{name: "valid score", req: oneQ(okState, "q", jev.Question{Type: jev.TypeScore, Instructions: j(okInstr), Criteria: j(`["low","medium","high"]`)})},
		{name: "valid noul no criteria", req: oneQ(okState, "q", noulQ())},
		{name: "valid noul null criteria", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(okInstr), Criteria: j(`null`)})},
		{name: "valid noul true false", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(okInstr), Criteria: j(`{"true":"yes","false":"no"}`)})},
		{name: "object state", req: oneQ(`{"user":"ada"}`, "q", noulQ())},
		{name: "array state", req: oneQ(`["a","b"]`, "q", noulQ())},
		{name: "object instructions", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`{"question":"decide"}`)})},
		{name: "array instructions", req: oneQ(okState, "q", jev.Question{Type: jev.TypeNoul, Instructions: j(`["decide","now"]`)})},

		// valid cases at the exact bounds, so the upper/lower guards have a
		// passing case on the valid side (an off-by-one <= / >= mutation fails).
		{name: "choice at max options", req: choiceWithOptions(jev.MaxChoiceOptions)},
		{name: "score at min levels", req: scoreWithLevels(jev.MinScoreLevels)},
		{name: "score at max levels", req: scoreWithLevels(jev.MaxScoreLevels)},
		{name: "questions at max", req: manyQuestions(jev.MaxQuestions)},
		{name: "name at max length", req: oneQ(okState, strings.Repeat("n", jev.MaxQuestionNameBytes), noulQ())},
		{name: "state at max size", req: maxState()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := jev.Validate(tt.req)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.substr)
			}
			if !errors.Is(err, jev.ErrValidation) {
				t.Errorf("Validate() error %v does not wrap ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tt.substr)
			}
		})
	}
}

func TestValidateMultiQuestion(t *testing.T) {
	t.Parallel()

	req := jev.Request{
		Model: "jev-latest",
		State: j(`{"ticket":"cannot log in"}`),
		Questions: map[string]jev.Question{
			"tone":      {Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`{"calm":"relaxed","frustrated":"annoyed"}`)},
			"severity":  {Type: jev.TypeScore, Instructions: j(`"how severe"`), Criteria: j(`["low","medium","high"]`)},
			"returning": {Type: jev.TypeNoul, Instructions: j(`"contacted before"`)},
		},
	}
	if err := jev.Validate(req); err != nil {
		t.Fatalf("Validate(valid multi-question request) = %v, want nil", err)
	}
}

func FuzzValidate(f *testing.F) {
	seeds := []string{
		`{"model":"m","state":"hi","questions":{"q":{"type":"noul","instructions":"decide"}}}`,
		`{"state":"s","questions":{"q":{"type":"choice","instructions":"d","criteria":{"a":null,"b":null}}}}`,
		`{"state":"s","questions":{"q":{"type":"score","instructions":"d","criteria":["lo","hi"]}}}`,
		`{}`,
		`null`,
		`{"state":null}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var req jev.Request
		if err := json.Unmarshal(data, &req); err != nil {
			return // a request that will not decode is not Validate's concern
		}
		// Validate must never panic, and any error it returns must wrap
		// ErrValidation so callers can match it with errors.Is.
		if err := jev.Validate(req); err != nil && !errors.Is(err, jev.ErrValidation) {
			t.Fatalf("Validate returned an error not wrapping ErrValidation: %v", err)
		}
	})
}

func BenchmarkValidate(b *testing.B) {
	req := jev.Request{
		Model: "jev-latest",
		State: j(`{"ticket":"cannot log in","user":"ada"}`),
		Questions: map[string]jev.Question{
			"tone":      {Type: jev.TypeChoice, Instructions: j(okInstr), Criteria: j(`{"calm":"relaxed","frustrated":"annoyed","angry":"hostile"}`)},
			"severity":  {Type: jev.TypeScore, Instructions: j(`"how severe"`), Criteria: j(`["low","medium","high"]`)},
			"returning": {Type: jev.TypeNoul, Instructions: j(`"contacted before"`)},
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := jev.Validate(req); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}
