// Package jev is the client for TypeSafe's Jev decision API. It owns the wire
// types, request validation, and the HTTP client with retry
// and provider fallback. It knows the wire format and nothing about the
// process environment or MCP.
//
// The request and response JSON is identical across the TypeSafe native API
// and OpenRouter; only the base URL, bearer key, and model-id normalisation
// differ. That is why one codec serves both backends. The wire facts here (the
// primitive field shapes and the OpenRouter-only response extras) were verified
// against docs.typesafe.ai and the OpenRouter System One docs on 2026-09-21.
package jev

import (
	"encoding/json"
	"fmt"
)

// QuestionType names one of Jev's three decision primitives.
type QuestionType string

// The three Jev decision primitives.
const (
	// TypeChoice picks exactly one option from a predefined set.
	TypeChoice QuestionType = "choice"
	// TypeScore places the state on an ordered scale.
	TypeScore QuestionType = "score"
	// TypeNoul answers a binary yes/no proposition.
	TypeNoul QuestionType = "noul"
)

// Question is one decision to evaluate against the shared state. Instructions
// and Criteria are carried as raw JSON so a string, object, or array all pass
// through unchanged; [Validate] enforces the per-type shape.
type Question struct {
	Type         QuestionType    `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	// Criteria is choice: an object of option to description; score: an ordered
	// array of level descriptions; noul: an optional object with true/false
	// keys.
	Criteria json.RawMessage `json:"criteria,omitempty"`
}

// Request is a Jev evaluation: one state and one or more named questions, all
// evaluated against that same state in a single parallel pass.
type Request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer is the result for one question. A well-formed Jev response populates
// only the fields that belong to the question's type; this codec does not
// enforce that, so callers switch on Type. Raw keeps the verbatim answer object
// so an unknown field or an unrecognised type is neither lost nor an error.
type Answer struct {
	Type       QuestionType `json:"type"`
	Choice     string       `json:"choice,omitempty"`
	Score      *float64     `json:"score,omitempty"`
	Noul       *float64     `json:"noul,omitempty"`
	Confidence *float64     `json:"confidence,omitempty"`
	// Probabilities is keyed by option name (choice) or by level index as a
	// string, for example "0", "1" (score).
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Legend maps a score level index to the supplied description. The value is
	// a string when the criteria level was a string and an object when it was
	// an object, so it stays raw.
	Legend map[string]json.RawMessage `json:"legend,omitempty"`
	// Raw is the verbatim answer object. It is populated by
	// [Response.UnmarshalJSON], not by a JSON tag.
	Raw json.RawMessage `json:"-"`
}

// Usage is the token accounting for a call. Cost is reported by OpenRouter
// only.
type Usage struct {
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	Cost         *float64 `json:"cost,omitempty"`
}

// Response is a Jev evaluation result. ID and Provider are set by OpenRouter
// only.
type Response struct {
	ID       string            `json:"id,omitempty"`
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model"`
	Answers  map[string]Answer `json:"answers"`
	Usage    Usage             `json:"usage"`
}

// UnmarshalJSON decodes a Response, preserving each answer's verbatim bytes in
// [Answer.Raw]. Answers are taken as raw JSON and decoded one at a time, so an
// answer this codec cannot fully model (an unrecognised type, or a known field
// carrying an unexpected shape) is kept in Raw rather than failing the whole
// response. A non-object answer is still an error.
func (r *Response) UnmarshalJSON(data []byte) error {
	// The envelope fields are listed here and must match Response; the
	// OpenRouter fixture exercises every one. Answers stay raw so a single
	// unmodellable answer cannot fail the decode of the rest.
	var env struct {
		ID       string                     `json:"id,omitempty"`
		Provider string                     `json:"provider,omitempty"`
		Model    string                     `json:"model"`
		Answers  map[string]json.RawMessage `json:"answers"`
		Usage    Usage                      `json:"usage"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}

	r.ID, r.Provider, r.Model, r.Usage = env.ID, env.Provider, env.Model, env.Usage
	r.Answers = nil
	if env.Answers == nil {
		return nil
	}
	r.Answers = make(map[string]Answer, len(env.Answers))
	for name, raw := range env.Answers {
		ans, err := decodeAnswer(raw)
		if err != nil {
			return fmt.Errorf("answer %q: %w", name, err)
		}
		r.Answers[name] = ans
	}
	return nil
}

// decodeAnswer decodes one answer, always preserving its verbatim bytes in Raw.
// The typed fields are best effort: an object this codec cannot fully model (an
// unrecognised type, or a known field with an unexpected shape) keeps its raw
// form and recovers the type discriminator where it can, rather than failing. A
// non-object answer is malformed and returns an error. raw is a map value that
// encoding/json decoded into fresh storage, so it already owns its bytes and is
// safe to keep without a clone.
func decodeAnswer(raw json.RawMessage) (Answer, error) {
	// Guard before decoding: json.Unmarshal accepts JSON null into a struct as a
	// no-op (no error), so a null answer would slip past a post-decode check.
	// Rejecting every non-object here keeps null, arrays, and scalars uniform.
	if jsonKind(raw) != '{' {
		return Answer{}, fmt.Errorf("%w: answer value is not a JSON object", ErrMalformedResponse)
	}
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		// An object this codec cannot fully model (an unrecognised type, or a
		// known field with an unexpected shape): keep it verbatim and recover
		// the type discriminator where possible.
		a = Answer{}
		var typed struct {
			Type QuestionType `json:"type"`
		}
		_ = json.Unmarshal(raw, &typed) // best effort; Type stays empty if it fails
		a.Type = typed.Type
	}
	a.Raw = raw
	return a, nil
}
