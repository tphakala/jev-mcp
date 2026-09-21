package jev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Request limits. The choice maximum (255) and the score range (2 to 10) are
// documented by the TypeSafe Jev API and were verified against
// docs.typesafe.ai on 2026-09-21; the remaining bounds are local guards, each
// noted as such.
const (
	// MaxStateBytes bounds the encoded state, in bytes, so one call cannot
	// carry an unbounded payload. It is a local guard (1 MiB).
	MaxStateBytes = 1 << 20
	// MaxQuestions bounds the number of questions per call. The API cap is
	// unverified; this is a local guard.
	MaxQuestions = 64
	// MaxQuestionNameBytes bounds a question name, in bytes.
	MaxQuestionNameBytes = 128
	// MinChoiceOptions is a local lower bound: a choice with one option has no
	// decision to make. The API documents only the maximum.
	MinChoiceOptions = 2
	// MaxChoiceOptions is the documented maximum number of choice options.
	MaxChoiceOptions = 255
	// MinScoreLevels is the documented minimum length of a score scale.
	MinScoreLevels = 2
	// MaxScoreLevels is the documented maximum length of a score scale.
	MaxScoreLevels = 10
)

// Validate reports whether req is well formed for the Jev API. Every failure
// wraps [ErrValidation]; where a single question is at fault the message names
// it, so the MCP tool can hand the caller a precise reason. Validate does no
// network I/O.
func Validate(req Request) error {
	if err := validateState(req.State); err != nil {
		return err
	}
	if n := len(req.Questions); n < 1 || n > MaxQuestions {
		return reqErr(fmt.Sprintf("need 1 to %d questions, got %d", MaxQuestions, n))
	}
	for name, q := range req.Questions {
		if err := validateQuestion(name, q); err != nil {
			return err
		}
	}
	return nil
}

func validateState(state json.RawMessage) error {
	switch {
	case len(state) == 0:
		return reqErr("state is required")
	case isJSONNull(state):
		return reqErr("state must not be null")
	case len(state) > MaxStateBytes:
		return reqErr(fmt.Sprintf("state is %d bytes, over the %d limit", len(state), MaxStateBytes))
	case !json.Valid(state):
		// The criteria and instructions validators reject malformed JSON as a
		// side effect of json.Unmarshal; state is never unmarshalled, so it is
		// checked explicitly to keep the treatment consistent.
		return reqErr("state must be valid JSON")
	}
	return nil
}

func validateQuestion(name string, q Question) error {
	if err := validateQuestionName(name); err != nil {
		return err
	}
	if err := validateInstructions(name, q.Instructions); err != nil {
		return err
	}
	switch q.Type {
	case TypeChoice:
		return validateChoiceCriteria(name, q.Criteria)
	case TypeScore:
		return validateScoreCriteria(name, q.Criteria)
	case TypeNoul:
		return validateNoulCriteria(name, q.Criteria)
	default:
		return qErr(name, fmt.Sprintf("unknown type %q", q.Type))
	}
}

func validateQuestionName(name string) error {
	switch {
	case name == "":
		return reqErr("a question name must not be empty")
	case len(name) > MaxQuestionNameBytes:
		return qErr(name, fmt.Sprintf("name is %d bytes, over the %d limit", len(name), MaxQuestionNameBytes))
	case strings.ContainsFunc(name, unicode.IsControl):
		return qErr(name, "name contains control characters")
	}
	return nil
}

func validateInstructions(name string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return qErr(name, "instructions are required")
	}
	switch jsonKind(raw) {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return qErr(name, "instructions string is malformed")
		}
		if strings.TrimSpace(s) == "" {
			return qErr(name, "instructions string must not be blank")
		}
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return qErr(name, "instructions object is malformed")
		}
		if len(obj) == 0 {
			return qErr(name, "instructions object must not be empty")
		}
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return qErr(name, "instructions array is malformed")
		}
		if len(arr) == 0 {
			return qErr(name, "instructions array must not be empty")
		}
	default:
		return qErr(name, "instructions must be a string, object, or array")
	}
	return nil
}

func validateChoiceCriteria(name string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return qErr(name, "choice requires criteria: an object of option to description")
	}
	var opts map[string]json.RawMessage
	if err := json.Unmarshal(raw, &opts); err != nil {
		return qErr(name, "choice criteria must be an object of option to description")
	}
	if n := len(opts); n < MinChoiceOptions || n > MaxChoiceOptions {
		return qErr(name, fmt.Sprintf("choice needs %d to %d options, got %d", MinChoiceOptions, MaxChoiceOptions, n))
	}
	for opt := range opts {
		if strings.TrimSpace(opt) == "" {
			return qErr(name, "choice option names must not be empty")
		}
	}
	return nil
}

func validateScoreCriteria(name string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return qErr(name, "score requires criteria: an ordered array of level descriptions")
	}
	var levels []json.RawMessage
	if err := json.Unmarshal(raw, &levels); err != nil {
		return qErr(name, "score criteria must be an ordered array of level descriptions")
	}
	if n := len(levels); n < MinScoreLevels || n > MaxScoreLevels {
		return qErr(name, fmt.Sprintf("score needs %d to %d levels, got %d", MinScoreLevels, MaxScoreLevels, n))
	}
	// A null level is rejected where a null choice description (validateChoice-
	// Criteria) is allowed: a choice option's identity is its key, so a missing
	// description is fine, but a score level's identity is its description, so a
	// null level is meaningless.
	for i, lvl := range levels {
		if isJSONNull(lvl) {
			return qErr(name, fmt.Sprintf("score level %d must not be null", i))
		}
	}
	return nil
}

func validateNoulCriteria(name string, raw json.RawMessage) error {
	// Criteria is optional for noul; both absent and JSON null mean "no rubric".
	if len(raw) == 0 || isJSONNull(raw) {
		return nil
	}
	var kv map[string]json.RawMessage
	if err := json.Unmarshal(raw, &kv); err != nil {
		return qErr(name, "noul criteria must be an object with true and false descriptions")
	}
	for key := range kv {
		if key != "true" && key != "false" {
			return qErr(name, fmt.Sprintf("noul criteria keys must be true or false, got %q", key))
		}
	}
	return nil
}

// qErr wraps ErrValidation with the offending question name and a detail.
func qErr(name, detail string) error {
	return fmt.Errorf("%w: question %q: %s", ErrValidation, name, detail)
}

// reqErr wraps ErrValidation with a request-level detail.
func reqErr(detail string) error {
	return fmt.Errorf("%w: %s", ErrValidation, detail)
}

// isJSONNull reports whether raw is the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// jsonKind returns the first significant byte of raw, which identifies the JSON
// value kind ('"' string, '{' object, '[' array, and so on), or 0 when raw
// holds no value.
func jsonKind(raw json.RawMessage) byte {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}
