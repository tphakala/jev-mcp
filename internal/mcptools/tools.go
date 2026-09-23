// Package mcptools adapts the Jev client to an MCP tool and hosts the stdio and
// Streamable HTTP transports. It maps MCP input to a jev.Request and the
// jev.Result back to tool output; the wire format, validation, retry, and
// fallback all stay in package jev.
package mcptools

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	jevmcp "github.com/tphakala/jev-mcp"
	"github.com/tphakala/jev-mcp/internal/jev"
)

// toolEvaluate is the tool name, shared between registration and the tests so
// the two cannot drift.
const toolEvaluate = "jev_evaluate"

// Render modes for the human-readable text of a jev_evaluate result. The
// structured result always carries the full output; only the text varies.
const (
	detailSummary = "summary"
	detailFull    = "full"
)

// JSON Schema type names used to spell out the union-typed input fields.
const (
	jsonString = "string"
	jsonObject = "object"
	jsonArray  = "array"
	jsonNull   = "null"
)

// summaryPrecision is the number of decimal places kept for the numbers in the
// summary text.
const summaryPrecision = 3

// Log messages are constants; variable data goes in attributes.
const (
	logMsgEvaluated = "jev_evaluate completed"
	logMsgFailed    = "jev_evaluate failed"
)

// ErrInvalidInput is returned for tool input the handler rejects before the
// request reaches the Jev client, for example a duplicate question name. The
// SDK hands its message back to the caller as a tool error.
var ErrInvalidInput = errors.New("jev_evaluate: invalid input")

// Evaluator is the part of *jev.Client the tool uses, so tests can substitute
// a fake without a network.
type Evaluator interface {
	Evaluate(ctx context.Context, req jev.Request) (*jev.Result, error)
}

// Deps is what the MCP server needs to answer tool calls.
type Deps struct {
	// Client runs evaluations.
	Client Evaluator
	// DefaultModel is used when a call omits model.
	DefaultModel string
	// Logger receives one record for each call that reaches the Jev client. A
	// nil Logger discards.
	Logger *slog.Logger
}

// questionInput is one question in a jev_evaluate call.
//
// The jsonschema tags are the per-property descriptions the client sees. The
// union-typed fields (instructions, criteria) get explicit JSON types, and the
// descriptions that quote a limit are rewritten from the jev constants, in
// evaluateInputSchema.
type questionInput struct {
	Name         string `json:"name"`
	Type         string `json:"type" jsonschema:"choice picks exactly one option from criteria; score places the state on the ordered scale given by criteria; noul answers a yes/no proposition"`
	Instructions any    `json:"instructions" jsonschema:"the question to decide, evaluated against state. A plain string is usual; an object (keys such as question, focus, note, or field) or an array is also accepted"`
	Criteria     any    `json:"criteria,omitempty"`
}

// evaluateInput is the input for jev_evaluate.
type evaluateInput struct {
	State     any             `json:"state" jsonschema:"the program state to decide over: a string, or a JSON object or array whose named parts give the model context. Every question is evaluated against this same state in one parallel pass. Text only"`
	Questions []questionInput `json:"questions"`
	Model     string          `json:"model,omitempty" jsonschema:"Jev model id: jev-latest, jev-preview, or a pinned version (jev-1.13.0 on TypeSafe, jev-1.13 on OpenRouter). Omit to use the server default"`
	Detail    string          `json:"detail,omitempty" jsonschema:"how much the text result shows: summary (the default) gives each answer with its confidence; full also gives probabilities, legend, provider, model, usage, and latency. The structured result always carries everything"`
}

// answerOutput is the answer to one question.
type answerOutput struct {
	Name          string             `json:"name" jsonschema:"the question name from the input"`
	Type          string             `json:"type" jsonschema:"choice, score, or noul, echoing the question"`
	Choice        string             `json:"choice,omitempty" jsonschema:"choice only: the selected option name, one of the criteria keys"`
	Score         *float64           `json:"score,omitempty" jsonschema:"score only: probability-weighted position on the scale, from 0 to the highest level index"`
	Noul          *float64           `json:"noul,omitempty" jsonschema:"noul only: probability from 0 to 1 that the proposition is true; this is both the answer and its certainty"`
	Confidence    *float64           `json:"confidence,omitempty" jsonschema:"choice and score: certainty from 0 to 1; gate on it before acting"`
	Probabilities map[string]float64 `json:"probabilities,omitempty" jsonschema:"choice: probability per option; score: probability per level index as a string key"`
	Legend        map[string]any     `json:"legend,omitempty" jsonschema:"score only: level index to the description supplied in criteria"`
	Raw           any                `json:"raw,omitempty" jsonschema:"the verbatim answer, present only when this server cannot read it: an unrecognised type, or a known type whose decision field is missing or malformed"`
}

// usageOutput is the token accounting for a call.
type usageOutput struct {
	InputTokens  int      `json:"input_tokens" jsonschema:"tokens billed for state and questions"`
	OutputTokens int      `json:"output_tokens" jsonschema:"answer tokens"`
	Cost         *float64 `json:"cost,omitempty" jsonschema:"USD cost, when the provider reports it"`
}

// evaluateOutput is the structured result of jev_evaluate.
type evaluateOutput struct {
	Provider  string         `json:"provider" jsonschema:"the backend that answered, typesafe or openrouter, after any fallback"`
	Model     string         `json:"model" jsonschema:"the model id the provider reports"`
	Answers   []answerOutput `json:"answers" jsonschema:"the answers the provider returned, in the order the questions were given; a question the provider did not answer is absent"`
	Usage     usageOutput    `json:"usage" jsonschema:"token accounting for this call"`
	LatencyMS int64          `json:"latency_ms" jsonschema:"wall-clock milliseconds of the provider call that answered"`
	Attempts  int            `json:"attempts" jsonschema:"HTTP attempts made across retries and fallback; 1 is the normal case"`
}

// annDecide: the tool writes nothing anywhere, so readOnlyHint is literal, and
// it reaches a paid external API, so the world is open. destructiveHint and
// idempotentHint are meaningful only when readOnlyHint is false (see
// mcp.ToolAnnotations), so they are left unset.
var annDecide = &mcp.ToolAnnotations{
	ReadOnlyHint:  true,
	OpenWorldHint: new(true),
}

const serverInstructions = `jev-mcp gives you typed, probabilistic decisions from Jev, a fast non-generative model: you pass a state and one or more questions and get back answers shaped by the criteria you defined. A choice answer is always one of your options; score and noul answers are numbers on the scale you defined. It does not write text.

Use jev_evaluate for routing, classification, triage, gating, and scoring: "which of these options fits", "where on this scale", "is this true". It typically answers in under a second and costs far less than a language-model call, so it suits decisions made many times or inside a loop. Do not use it to generate, summarise, or explain.

Batch related questions over the same state into one call. Gate on confidence (or on the noul probability) before acting on an answer. When it is low and you need the alternatives, read the probabilities from the structured result; if your client shows you only the text, call again with detail set to full.

State and instructions are sent to an external API (TypeSafe, or OpenRouter). Do not include secrets.`

// evaluateDescription is the tool description. The score level range comes
// from the jev constants so it cannot drift from what jev.Validate enforces.
var evaluateDescription = fmt.Sprintf(`Decide one or more questions over a shared state with Jev, returning typed answers with probabilities. Three question types: choice (pick one option from criteria, an object of option name to description), score (place the state on criteria, an ordered array of %d to %d levels, lowest first), and noul (a yes/no proposition; its answer is the probability of yes). All questions in a call are evaluated together against the same state. The text result is a summary of each answer and its confidence unless detail is full; the structured result always carries everything.`,
	jev.MinScoreLevels, jev.MaxScoreLevels)

// NewServer builds the MCP server with jev_evaluate registered.
func NewServer(d Deps) *mcp.Server {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	// ServerOptions.Logger stays unset, so the SDK discards its own records:
	// they are preformatted strings, and a normal shutdown is logged at Error
	// ("server run cancelled", go-sdk v1.8.0 mcp/server.go Server.Run).
	s := mcp.NewServer(
		&mcp.Implementation{Name: "jev-mcp", Version: jevmcp.Version},
		&mcp.ServerOptions{Instructions: serverInstructions},
	)
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolEvaluate,
		Title:       "Decide with Jev",
		Description: evaluateDescription,
		Annotations: annDecide,
		InputSchema: evaluateInputSchema(),
	}, d.evaluate)
	return s
}

// evaluateInputSchema derives the input schema from evaluateInput and then
// states what the Go types cannot: the JSON types a union-typed field accepts
// (an interface field is otherwise rendered with no type at all), the enums,
// the question bounds, and the descriptions that quote a jev limit. The SDK
// validates arguments against this schema before the handler runs.
func evaluateInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[evaluateInput](nil)
	if err != nil {
		// evaluateInput is a fixed type; a failure here is a programming error
		// caught by every test that builds the server.
		panic(fmt.Sprintf("mcptools: derive jev_evaluate input schema: %v", err))
	}
	s.Properties["state"].Types = []string{jsonString, jsonObject, jsonArray}
	s.Properties["detail"].Enum = []any{detailSummary, detailFull}

	questions := s.Properties["questions"]
	// A slice is derived as ["null", "array"]; null is not a valid value.
	questions.Types = nil
	questions.Type = jsonArray
	questions.MinItems = new(1)
	questions.MaxItems = new(jev.MaxQuestions)
	questions.Description = fmt.Sprintf("the decisions to make over state, 1 to %d per call. Batching related decisions into one call is cheaper and faster than several calls", jev.MaxQuestions)

	q := questions.Items
	name := q.Properties["name"]
	name.MinLength = new(1)
	// maxLength counts characters and jev counts bytes, so the schema bound is
	// looser for non-ASCII names; jev.Validate enforces the byte limit.
	name.MaxLength = new(jev.MaxQuestionNameBytes)
	name.Description = fmt.Sprintf("a name for this question, unique within the call and at most %d bytes; its answer comes back under the same name", jev.MaxQuestionNameBytes)
	q.Properties["type"].Enum = []any{string(jev.TypeChoice), string(jev.TypeScore), string(jev.TypeNoul)}
	q.Properties["instructions"].Types = []string{jsonString, jsonObject, jsonArray}
	criteria := q.Properties["criteria"]
	criteria.Types = []string{jsonObject, jsonArray, jsonNull}
	criteria.Description = fmt.Sprintf("choice (required): an object mapping each option name to its description (%d to %d options); a description may be a string, an object such as {what, not_for, examples}, or null. score (required): an ordered array of %d to %d level descriptions, index 0 lowest. noul (optional): an object with true and false descriptions, or omitted",
		jev.MinChoiceOptions, jev.MaxChoiceOptions, jev.MinScoreLevels, jev.MaxScoreLevels)
	return s
}

// rawQuestion and rawInput mirror questionInput and evaluateInput with the
// union-typed fields kept as raw JSON. The SDK decodes the arguments into a
// map[string]any and marshals them again before validating them (go-sdk
// v1.8.0 mcp/tool.go applySchema), which turns every JSON number into a
// float64 and re-sorts object keys, so an integer above 2^53 in state would
// reach Jev rounded. The handler therefore builds the request from the
// arguments as the client sent them (CallToolParamsRaw.Arguments), after the
// SDK has validated them against the input schema. The schema declares no
// defaults, so the raw arguments carry everything the validated input does.
type rawQuestion struct {
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type rawInput struct {
	State     json.RawMessage
	Questions []rawQuestion
	Model     string
	Detail    string
}

// rawEnvelope is the wire shape rawInput is decoded from. Questions stay raw
// here and are decoded one by one into fresh values: encoding/json decodes a
// repeated key into the slice it already filled, so a field the last copy of
// questions omits would otherwise keep the first copy's value, while the SDK
// validated only the last copy.
type rawEnvelope struct {
	State     json.RawMessage   `json:"state"`
	Questions []json.RawMessage `json:"questions"`
	Model     string            `json:"model,omitempty"`
	Detail    string            `json:"detail,omitempty"`
}

// jsonNullLiteral is the JSON null token.
var jsonNullLiteral = []byte("null")

// decodeRawInput decodes the tool arguments into a rawInput. A null criteria
// is treated as omitted, so it is not sent to Jev; the schema accepts null
// for callers that spell an absent value that way.
func decodeRawInput(args json.RawMessage) (rawInput, error) {
	var env rawEnvelope
	if err := json.Unmarshal(args, &env); err != nil {
		return rawInput{}, err
	}
	in := rawInput{
		State:     env.State,
		Model:     env.Model,
		Detail:    env.Detail,
		Questions: make([]rawQuestion, len(env.Questions)),
	}
	for i, raw := range env.Questions {
		q := &in.Questions[i]
		if err := json.Unmarshal(raw, q); err != nil {
			return rawInput{}, fmt.Errorf("question %d: %w", i, err)
		}
		if bytes.Equal(bytes.TrimSpace(q.Criteria), jsonNullLiteral) {
			q.Criteria = nil
		}
	}
	return in, nil
}

// evaluate is the jev_evaluate handler. The typed input exists for the SDK's
// schema validation; the request is built from the raw arguments (see
// rawInput).
func (d Deps) evaluate(ctx context.Context, call *mcp.CallToolRequest, _ evaluateInput) (*mcp.CallToolResult, evaluateOutput, error) {
	in, err := decodeRawInput(call.Params.Arguments)
	if err != nil {
		return nil, evaluateOutput{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	detail, req, err := d.toRequest(&in)
	if err != nil {
		return nil, evaluateOutput{}, err
	}
	res, err := d.Client.Evaluate(ctx, req)
	if err != nil {
		d.logFailed(ctx, len(req.Questions), err)
		return nil, evaluateOutput{}, err
	}
	names := questionNames(in.Questions)
	out, err := toOutput(res, names)
	var text string
	if err == nil && detail != detailFull {
		text, err = summaryText(res, names)
	}
	if err != nil {
		d.logFailed(ctx, len(req.Questions), err)
		return nil, evaluateOutput{}, err
	}
	d.Logger.InfoContext(ctx, logMsgEvaluated,
		slog.String("provider", res.ProviderName),
		slog.String("model", res.Model),
		slog.Int("questions", len(req.Questions)),
		slog.Int("attempts", res.Attempts),
		slog.Duration("latency", res.Latency))
	if detail == detailFull {
		// A nil result makes the SDK mirror the full structured output into the
		// text content.
		return nil, out, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}

// logFailed records a call that reached the Jev client and then failed. Only
// the question count and the error are logged, never request content.
func (d Deps) logFailed(ctx context.Context, questions int, err error) {
	d.Logger.WarnContext(ctx, logMsgFailed,
		slog.Int("questions", questions),
		slog.String("error", err.Error()))
}

// toRequest rejects what the schema cannot express (a duplicate question
// name), re-checks detail for callers that bypass the schema, and builds the
// Jev request. The per-question rules live in jev.Validate, which
// Client.Evaluate runs, so they are not repeated here.
func (d Deps) toRequest(in *rawInput) (string, jev.Request, error) {
	detail := in.Detail
	switch detail {
	case "":
		detail = detailSummary
	case detailSummary, detailFull:
	default:
		return "", jev.Request{}, fmt.Errorf("%w: detail must be %s or %s, got %q", ErrInvalidInput, detailSummary, detailFull, detail)
	}

	req := jev.Request{
		Model:     cmp.Or(in.Model, d.DefaultModel),
		State:     in.State,
		Questions: make(map[string]jev.Question, len(in.Questions)),
	}
	for _, q := range in.Questions {
		if _, dup := req.Questions[q.Name]; dup {
			return "", jev.Request{}, fmt.Errorf("%w: question name %q is used more than once", ErrInvalidInput, q.Name)
		}
		req.Questions[q.Name] = jev.Question{
			Type:         jev.QuestionType(q.Type),
			Instructions: q.Instructions,
			Criteria:     q.Criteria,
		}
	}
	return detail, req, nil
}

// questionNames returns the question names in input order.
func questionNames(qs []rawQuestion) []string {
	names := make([]string, len(qs))
	for i, q := range qs {
		names[i] = q.Name
	}
	return names
}

// orderedAnswerNames returns the answer names to report: the asked names that
// have an answer, in input order, followed by any answer the provider returned
// for a name that was not asked, in sorted order. A question the provider did
// not answer is left out rather than reported with an invented value.
func orderedAnswerNames(answers map[string]jev.Answer, asked []string) []string {
	out := make([]string, 0, len(answers))
	seen := make(map[string]bool, len(asked))
	for _, name := range asked {
		seen[name] = true
		if _, ok := answers[name]; ok {
			out = append(out, name)
		}
	}
	var extra []string
	for name := range answers {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	return append(out, extra...)
}

// readable reports whether a carries the decision for its type: a choice with
// an option, a score or a noul with its number. It is false for a type this
// server does not model, and for a known type whose fields failed to decode,
// which jev.Response.UnmarshalJSON reports by keeping only Type and Raw. Such
// an answer is passed through verbatim instead of being shown as a decision
// with no value.
func readable(a *jev.Answer) bool {
	switch a.Type {
	case jev.TypeChoice:
		return a.Choice != ""
	case jev.TypeScore:
		return a.Score != nil
	case jev.TypeNoul:
		return a.Noul != nil
	}
	return false
}

// toOutput converts a Jev result to the structured tool output.
func toOutput(res *jev.Result, asked []string) (evaluateOutput, error) {
	out := evaluateOutput{
		Provider: res.ProviderName,
		Model:    res.Model,
		Answers:  make([]answerOutput, 0, len(res.Answers)),
		Usage: usageOutput{
			InputTokens:  res.Usage.InputTokens,
			OutputTokens: res.Usage.OutputTokens,
			Cost:         res.Usage.Cost,
		},
		LatencyMS: res.Latency.Round(time.Millisecond).Milliseconds(),
		Attempts:  res.Attempts,
	}
	for _, name := range orderedAnswerNames(res.Answers, asked) {
		a := res.Answers[name]
		ao := answerOutput{Name: name, Type: string(a.Type)}
		if !readable(&a) {
			// Like the summary, an answer this server cannot read carries only
			// its verbatim form, not whichever typed fields happened to decode.
			if err := json.Unmarshal(a.Raw, &ao.Raw); err != nil {
				return evaluateOutput{}, fmt.Errorf("%w: answer %q: %w", jev.ErrMalformedResponse, name, err)
			}
			out.Answers = append(out.Answers, ao)
			continue
		}
		ao.Choice = a.Choice
		ao.Score = a.Score
		ao.Noul = a.Noul
		ao.Confidence = a.Confidence
		ao.Probabilities = a.Probabilities
		if len(a.Legend) > 0 {
			ao.Legend = make(map[string]any, len(a.Legend))
			for k, raw := range a.Legend {
				var v any
				if err := json.Unmarshal(raw, &v); err != nil {
					return evaluateOutput{}, fmt.Errorf("%w: answer %q legend %q: %w", jev.ErrMalformedResponse, name, k, err)
				}
				ao.Legend[k] = v
			}
		}
		out.Answers = append(out.Answers, ao)
	}
	return out, nil
}

// summaryAnswer is one answer in the summary text: the decision and its
// certainty, nothing else.
type summaryAnswer struct {
	Name       string          `json:"name"`
	Choice     string          `json:"choice,omitempty"`
	Score      *float64        `json:"score,omitempty"`
	Noul       *float64        `json:"noul,omitempty"`
	Confidence *float64        `json:"confidence,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// summaryText renders the compact text result: each answer's decision and
// confidence, in input order, with numbers rounded to summaryPrecision places.
// An answer that is not readable is shown verbatim under raw.
func summaryText(res *jev.Result, asked []string) (string, error) {
	answers := make([]summaryAnswer, 0, len(res.Answers))
	for _, name := range orderedAnswerNames(res.Answers, asked) {
		a := res.Answers[name]
		sa := summaryAnswer{Name: name}
		switch {
		case !readable(&a):
			sa.Raw = a.Raw
		case a.Type == jev.TypeChoice:
			sa.Choice = a.Choice
			sa.Confidence = roundPtr(a.Confidence)
		case a.Type == jev.TypeScore:
			sa.Score = roundPtr(a.Score)
			sa.Confidence = roundPtr(a.Confidence)
		default: // a readable noul
			sa.Noul = roundPtr(a.Noul)
		}
		answers = append(answers, sa)
	}
	b, err := json.Marshal(struct {
		Answers []summaryAnswer `json:"answers"`
	}{answers})
	if err != nil {
		return "", fmt.Errorf("mcptools: render summary: %w", err)
	}
	return string(b), nil
}

// roundPtr rounds *p to summaryPrecision decimal places, keeping nil as nil.
func roundPtr(p *float64) *float64 {
	if p == nil {
		return nil
	}
	scale := math.Pow10(summaryPrecision)
	r := math.Round(*p*scale) / scale
	if math.IsInf(r, 0) {
		// Scaling overflowed a value near the float64 limit; it has no
		// fractional digits to round away, so keep it as is.
		return p
	}
	return &r
}
