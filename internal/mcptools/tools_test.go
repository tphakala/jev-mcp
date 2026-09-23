package mcptools

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tphakala/jev-mcp/internal/jev"
)

// mixedResponse answers one question of each primitive plus one of a type this
// server does not recognise, with unrounded numbers and a legend mixing a
// string level and an object level.
const mixedResponse = `{"model":"jev-1.13.0","answers":{` +
	`"route":{"type":"choice","choice":"billing","confidence":0.87341,"probabilities":{"billing":0.87341,"tech":0.12659}},` +
	`"severity":{"type":"score","score":1.23456,"confidence":0.6,"probabilities":{"0":0.1,"1":0.5,"2":0.4},"legend":{"0":"low","1":{"what":"medium"},"2":"high"}},` +
	`"spam":{"type":"noul","noul":0.01234},` +
	`"future":{"type":"rank","order":["a","b"]}},` +
	`"usage":{"input_tokens":120,"output_tokens":4}}`

// mixedArgs asks the questions mixedResponse answers, in an order that is not
// sorted, so the tests can tell input order from map or name order.
func mixedArgs() map[string]any {
	return map[string]any{
		"state": map[string]any{"ticket": "I was charged twice"},
		"questions": []any{
			map[string]any{"name": "route", "type": "choice", "instructions": "which team", "criteria": map[string]any{"billing": "money", "tech": nil}},
			map[string]any{"name": "severity", "type": "score", "instructions": "how bad", "criteria": []any{"low", map[string]any{"what": "medium"}, "high"}},
			map[string]any{"name": "spam", "type": "noul", "instructions": "is it spam"},
			map[string]any{"name": "future", "type": "noul", "instructions": map[string]any{"question": "anything"}},
		},
	}
}

// structured decodes a tool result's structured content into evaluateOutput.
func structured(t *testing.T, res *mcp.CallToolResult) evaluateOutput {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var out evaluateOutput
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode structured content: %v\n%s", err, b)
	}
	return out
}

// answerNames lists the names of answers in order.
func answerNames(answers []answerOutput) []string {
	names := make([]string, 0, len(answers))
	for _, a := range answers {
		names = append(names, a.Name)
	}
	return names
}

func TestEvaluateSummaryText(t *testing.T) {
	t.Parallel()

	fake := &fakeEvaluator{res: resultFrom(t, mixedResponse)}
	res := callEvaluate(t, connect(t, Deps{Client: fake}), mixedArgs())
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}

	// Input order, three decimals, no confidence for noul, the unknown type
	// verbatim under raw, and nothing else (no probabilities, legend, usage).
	const want = `{"answers":[` +
		`{"name":"route","choice":"billing","confidence":0.873},` +
		`{"name":"severity","score":1.235,"confidence":0.6},` +
		`{"name":"spam","noul":0.012},` +
		`{"name":"future","raw":{"type":"rank","order":["a","b"]}}]}`
	if got := resultText(t, res); got != want {
		t.Fatalf("summary text:\n got %s\nwant %s", got, want)
	}
}

func TestEvaluateStructuredOutputIsFull(t *testing.T) {
	t.Parallel()

	fake := &fakeEvaluator{res: resultFrom(t, mixedResponse)}
	res := callEvaluate(t, connect(t, Deps{Client: fake}), mixedArgs())
	out := structured(t, res)

	if out.Provider != jev.ProviderTypeSafe || out.Model != "jev-1.13.0" || out.Attempts != 1 {
		t.Errorf("provider/model/attempts = %q/%q/%d", out.Provider, out.Model, out.Attempts)
	}
	if out.Usage.InputTokens != 120 || out.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v", out.Usage)
	}
	if names, want := answerNames(out.Answers), []string{"route", "severity", "spam", "future"}; !slices.Equal(names, want) {
		t.Fatalf("answer order = %v, want %v", names, want)
	}
	assertMixedAnswers(t, out.Answers)
}

// assertMixedAnswers checks the answers decoded from mixedResponse, in input
// order.
func assertMixedAnswers(t *testing.T, answers []answerOutput) {
	t.Helper()
	route, severity, spam, future := answers[0], answers[1], answers[2], answers[3]
	// Structured numbers are not rounded; only the summary text is.
	if route.Choice != "billing" || route.Confidence == nil || *route.Confidence != 0.87341 {
		t.Errorf("route = %+v", route)
	}
	if route.Probabilities["tech"] != 0.12659 {
		t.Errorf("route probabilities = %v", route.Probabilities)
	}
	wantLegend := map[string]any{"0": "low", "1": map[string]any{"what": "medium"}, "2": "high"}
	if !reflect.DeepEqual(severity.Legend, wantLegend) {
		t.Errorf("severity legend = %#v, want %#v", severity.Legend, wantLegend)
	}
	if spam.Noul == nil || *spam.Noul != 0.01234 || spam.Confidence != nil {
		t.Errorf("spam = %+v", spam)
	}
	if spam.Raw != nil || route.Raw != nil {
		t.Error("raw must be set only for an unrecognised answer type")
	}
	wantRaw := map[string]any{"type": "rank", "order": []any{"a", "b"}}
	if future.Type != "rank" || !reflect.DeepEqual(future.Raw, wantRaw) {
		t.Errorf("future = %+v, want type rank with raw %v", future, wantRaw)
	}
}

func TestEvaluateFullDetailMirrorsStructured(t *testing.T) {
	t.Parallel()

	fake := &fakeEvaluator{res: resultFrom(t, mixedResponse)}
	args := mixedArgs()
	args["detail"] = detailFull
	res := callEvaluate(t, connect(t, Deps{Client: fake}), args)
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}

	var fromText any
	if err := json.Unmarshal([]byte(resultText(t, res)), &fromText); err != nil {
		t.Fatalf("full text is not JSON: %v", err)
	}
	if !reflect.DeepEqual(fromText, res.StructuredContent) {
		t.Fatalf("full text differs from structured content:\ntext %v\nstructured %v", fromText, res.StructuredContent)
	}
	if !strings.Contains(resultText(t, res), `"probabilities"`) {
		t.Fatal("full text should carry probabilities")
	}
}

func TestEvaluateRequestMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     any
		wantModel string
	}{
		{name: "default model", model: nil, wantModel: "jev-default"},
		{name: "explicit model", model: "jev-1.13", wantModel: "jev-1.13"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeEvaluator{res: resultFrom(t, mixedResponse)}
			args := mixedArgs()
			if tt.model != nil {
				args["model"] = tt.model
			}
			callEvaluate(t, connect(t, Deps{Client: fake, DefaultModel: "jev-default"}), args)

			reqs := fake.requests()
			if len(reqs) != 1 {
				t.Fatalf("got %d requests, want 1", len(reqs))
			}
			req := reqs[0]
			if req.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", req.Model, tt.wantModel)
			}
			if got := string(req.State); got != `{"ticket":"I was charged twice"}` {
				t.Errorf("state = %s", got)
			}
			if got := string(req.Questions["route"].Criteria); got != `{"billing":"money","tech":null}` {
				t.Errorf("route criteria = %s", got)
			}
			if got := string(req.Questions["future"].Instructions); got != `{"question":"anything"}` {
				t.Errorf("future instructions = %s", got)
			}
			// An omitted criteria must stay absent, not become the literal null.
			if c := req.Questions["spam"].Criteria; c != nil {
				t.Errorf("spam criteria = %s, want absent", c)
			}
			if got := req.Questions["severity"].Type; got != jev.TypeScore {
				t.Errorf("severity type = %q", got)
			}
		})
	}
}

// TestEvaluateOrdersAnswers pins the answer order when the provider's answers
// do not match the questions: a missing answer is left out, and an answer for
// a name that was not asked follows the asked ones in name order.
func TestEvaluateOrdersAnswers(t *testing.T) {
	t.Parallel()

	const body = `{"model":"m","answers":{` +
		`"zeta":{"type":"noul","noul":0.5},` +
		`"b":{"type":"noul","noul":0.1},` +
		`"a":{"type":"noul","noul":0.2}},"usage":{"input_tokens":1,"output_tokens":1}}`
	fake := &fakeEvaluator{res: resultFrom(t, body)}
	args := map[string]any{
		"state": "s",
		"questions": []any{
			map[string]any{"name": "zeta", "type": "noul", "instructions": "q"},
			map[string]any{"name": "missing", "type": "noul", "instructions": "q"},
		},
	}
	res := callEvaluate(t, connect(t, Deps{Client: fake}), args)
	if names, want := answerNames(structured(t, res).Answers), []string{"zeta", "a", "b"}; !slices.Equal(names, want) {
		t.Fatalf("answer order = %v, want %v", names, want)
	}
	const wantText = `{"answers":[{"name":"zeta","noul":0.5},{"name":"a","noul":0.2},{"name":"b","noul":0.1}]}`
	if got := resultText(t, res); got != wantText {
		t.Fatalf("summary text = %s, want %s", got, wantText)
	}
}

func TestEvaluateToolErrors(t *testing.T) {
	t.Parallel()

	// A real client: validation runs inside Evaluate before any network call,
	// so the unreachable base URL is never dialled.
	realClient, err := jev.New([]jev.Provider{{
		Name: jev.ProviderTypeSafe, BaseURL: "http://127.0.0.1:1", Path: jev.SystemOnePath, APIKey: "k",
	}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	apiErr := &jev.APIError{Provider: jev.ProviderTypeSafe, Status: 422, Message: "criteria rejected", Sentinel: jev.ErrInvalidRequest}

	noul := func(name string) map[string]any {
		return map[string]any{"name": name, "type": "noul", "instructions": "q"}
	}
	tests := []struct {
		name     string
		client   Evaluator
		args     map[string]any
		wantText []string
	}{
		{
			name:     "duplicate question name",
			client:   &fakeEvaluator{},
			args:     map[string]any{"state": "s", "questions": []any{noul("same"), noul("same")}},
			wantText: []string{`"same"`, "more than once"},
		},
		{
			name:     "invalid detail",
			client:   &fakeEvaluator{},
			args:     map[string]any{"state": "s", "questions": []any{noul("q")}, "detail": "verbose"},
			wantText: []string{"detail"},
		},
		{
			name:     "missing questions",
			client:   &fakeEvaluator{},
			args:     map[string]any{"state": "s"},
			wantText: []string{"questions"},
		},
		{
			name:     "null state",
			client:   &fakeEvaluator{},
			args:     map[string]any{"state": nil, "questions": []any{noul("q")}},
			wantText: []string{"state"},
		},
		{
			name:   "validation names the question",
			client: realClient,
			args: map[string]any{"state": "s", "questions": []any{
				map[string]any{"name": "pick", "type": "choice", "instructions": "q", "criteria": map[string]any{"only": "one"}},
			}},
			wantText: []string{`"pick"`, "options"},
		},
		{
			name:     "provider rejection",
			client:   &fakeEvaluator{err: apiErr},
			args:     map[string]any{"state": "s", "questions": []any{noul("q")}},
			wantText: []string{"criteria rejected", "422"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := callEvaluate(t, connect(t, Deps{Client: tt.client}), tt.args)
			if !res.IsError {
				t.Fatalf("want a tool error, got success: %v", res.StructuredContent)
			}
			text := resultText(t, res)
			for _, want := range tt.wantText {
				if !strings.Contains(text, want) {
					t.Errorf("error text %q does not contain %q", text, want)
				}
			}
		})
	}
}

// TestToRequestRejectsInvalidDetail covers the handler's own detail check,
// which the schema enum normally shadows, so it stays correct if the schema
// is ever loosened.
func TestToRequestRejectsInvalidDetail(t *testing.T) {
	t.Parallel()

	_, _, err := Deps{}.toRequest(evaluateInput{State: "s", Detail: "verbose"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

// TestEvaluateInputSchema pins what the derived schema cannot express on its
// own: explicit JSON types for the union-typed fields (an interface field is
// otherwise rendered with no type), the enums, and the question bounds.
func TestEvaluateInputSchema(t *testing.T) {
	t.Parallel()

	res, err := connect(t, Deps{}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	b, err := json.Marshal(res.Tools[0].InputSchema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var s struct {
		Required   []string `json:"required"`
		Properties struct {
			State struct {
				Type []string `json:"type"`
			} `json:"state"`
			Detail struct {
				Enum []string `json:"enum"`
			} `json:"detail"`
			Questions struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required   []string `json:"required"`
					Properties struct {
						Name struct {
							MinLength int `json:"minLength"`
							MaxLength int `json:"maxLength"`
						} `json:"name"`
						Type struct {
							Enum []string `json:"enum"`
						} `json:"type"`
						Instructions struct {
							Type []string `json:"type"`
						} `json:"instructions"`
						Criteria struct {
							Type []string `json:"type"`
						} `json:"criteria"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"questions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode schema: %v\n%s", err, b)
	}
	p := s.Properties
	q := p.Questions.Items.Properties
	checks := []struct {
		what      string
		got, want []string
	}{
		{"required", s.Required, []string{"questions", "state"}},
		{"state type", p.State.Type, []string{"array", "object", "string"}},
		{"detail enum", p.Detail.Enum, []string{"full", "summary"}},
		{"question required", p.Questions.Items.Required, []string{"instructions", "name", "type"}},
		{"question type enum", q.Type.Enum, []string{"choice", "noul", "score"}},
		{"instructions type", q.Instructions.Type, []string{"array", "object", "string"}},
		{"criteria type", q.Criteria.Type, []string{"array", "object"}},
	}
	for _, c := range checks {
		got := slices.Sorted(slices.Values(c.got))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s = %v, want %v", c.what, got, c.want)
		}
	}
	if p.Questions.MinItems != 1 || p.Questions.MaxItems != jev.MaxQuestions {
		t.Errorf("questions items bounds = %d..%d, want 1..%d", p.Questions.MinItems, p.Questions.MaxItems, jev.MaxQuestions)
	}
	if q.Name.MinLength != 1 || q.Name.MaxLength != jev.MaxQuestionNameBytes {
		t.Errorf("name length bounds = %d..%d", q.Name.MinLength, q.Name.MaxLength)
	}
}
