// Package jev is the client for TypeSafe's Jev decision API. It owns the wire
// types, request validation, and (in later changes) the HTTP client with retry
// and provider fallback. It knows the wire format and nothing about the
// process environment or MCP.
//
// The request and response JSON is identical across the TypeSafe native API
// and OpenRouter; only the base URL, bearer key, and model-id normalisation
// differ. That is why one codec serves both backends. The wire facts here (the
// primitive field shapes and the OpenRouter-only response extras) were verified
// against docs.typesafe.ai and the OpenRouter System One docs on 2026-09-21.
package jev

import "encoding/json"

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

// UnmarshalJSON decodes a Response and preserves each answer's verbatim bytes
// in [Answer.Raw], so an unknown field or an unrecognised answer type survives
// a round trip. It decodes the payload twice: once with the default struct
// decoding for the typed fields, once to capture the raw answer objects.
func (r *Response) UnmarshalJSON(data []byte) error {
	// wire has no methods, so json uses the default struct decoding instead of
	// recursing into this method.
	type wire Response
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	var rawAnswers struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(data, &rawAnswers); err != nil {
		return err
	}
	for name, raw := range rawAnswers.Answers {
		ans := w.Answers[name]
		// raw is a map value decoded by encoding/json, which copies into fresh
		// backing storage (RawMessage.UnmarshalJSON does append((*m)[:0], data)),
		// so it already owns its bytes and never aliases data. No clone needed.
		ans.Raw = raw
		w.Answers[name] = ans
	}

	*r = Response(w)
	return nil
}
