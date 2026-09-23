package mcptools

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listRegisteredTools returns the tools/list payload a real client receives, so
// these assertions run against the wire shape rather than the registration
// literals. Listing never dispatches a handler, so Deps needs no Evaluator.
func listRegisteredTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	res, err := connect(t, Deps{}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	return res.Tools
}

// wantAnnotations is the expected annotation for every registered tool, written
// out field by field. The read-only and open-world claims are what this server
// tells its clients, so a silent flip must fail here.
var wantAnnotations = map[string]mcp.ToolAnnotations{
	toolEvaluate: {ReadOnlyHint: true, OpenWorldHint: new(true)},
}

// eqBoolPtr compares two optional hints. Nil is distinct from false: the MCP
// spec defaults destructiveHint and openWorldHint to true, so "unset" and
// "explicitly false" say different things on the wire.
func eqBoolPtr(got, want *bool) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

// TestToolDefinitionsDeclareAnnotations: absent annotations are not neutral.
// The MCP spec defaults them to destructiveHint=true, openWorldHint=true,
// readOnlyHint=false, so every tool must state its real behaviour.
func TestToolDefinitionsDeclareAnnotations(t *testing.T) {
	t.Parallel()

	tools := listRegisteredTools(t)
	// Require the two sets to match exactly, so a tool added without a
	// classification here fails rather than escaping review.
	if len(tools) != len(wantAnnotations) {
		t.Errorf("got %d tools, want %d; a tool was added or removed without updating wantAnnotations", len(tools), len(wantAnnotations))
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()
			want, known := wantAnnotations[tool.Name]
			if !known {
				t.Fatalf("tool %q is not classified here; add it to wantAnnotations", tool.Name)
			}
			got := tool.Annotations
			if got == nil {
				t.Fatal("no annotations: clients will assume destructive and open-world")
			}
			if got.ReadOnlyHint != want.ReadOnlyHint {
				t.Errorf("readOnlyHint = %v, want %v", got.ReadOnlyHint, want.ReadOnlyHint)
			}
			if got.IdempotentHint != want.IdempotentHint {
				t.Errorf("idempotentHint = %v, want %v", got.IdempotentHint, want.IdempotentHint)
			}
			if !eqBoolPtr(got.DestructiveHint, want.DestructiveHint) {
				t.Errorf("destructiveHint = %v, want %v", fmtHint(got.DestructiveHint), fmtHint(want.DestructiveHint))
			}
			if !eqBoolPtr(got.OpenWorldHint, want.OpenWorldHint) {
				t.Errorf("openWorldHint = %v, want %v", fmtHint(got.OpenWorldHint), fmtHint(want.OpenWorldHint))
			}
			// destructiveHint is meaningful only when readOnlyHint is false.
			if want.ReadOnlyHint && got.DestructiveHint != nil {
				t.Error("read-only tool should not carry a decorative destructiveHint")
			}
		})
	}
}

// fmtHint renders an optional hint so a failure distinguishes unset from false.
func fmtHint(b *bool) string {
	switch {
	case b == nil:
		return "unset"
	case *b:
		return "true"
	default:
		return "false"
	}
}

// TestToolDefinitionsDescribeThemselves guards the qualities a tool definition
// is judged on: a title, a description that is more than the tool's own name,
// and a description on every input and output property, since that is where
// per-field meaning belongs.
func TestToolDefinitionsDescribeThemselves(t *testing.T) {
	t.Parallel()

	for _, tool := range listRegisteredTools(t) {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()
			if tool.Title == "" {
				t.Error("no title")
			}
			desc := strings.TrimSpace(tool.Description)
			if desc == "" {
				t.Fatal("no description")
			}
			for _, tautology := range []string{
				tool.Name,
				strings.ReplaceAll(tool.Name, "_", " "),
				tool.Title,
			} {
				if strings.EqualFold(strings.TrimSuffix(desc, "."), tautology) {
					t.Errorf("description merely restates %q", tautology)
				}
			}
			assertPropertiesDescribed(t, "input", tool.InputSchema)
			assertPropertiesDescribed(t, "output", tool.OutputSchema)
		})
	}
}

// assertPropertiesDescribed requires every property of a schema to carry a
// description, recursing into array items and into additionalProperties, so
// the fields of a question or an answer cannot escape the requirement.
func assertPropertiesDescribed(t *testing.T, which string, raw any) {
	t.Helper()
	if raw == nil {
		t.Errorf("%s schema is absent", which)
		return
	}
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s schema has unexpected type %T", which, raw)
	}
	if extra, ok := schema["additionalProperties"].(map[string]any); ok {
		assertPropertiesDescribed(t, which+" additionalProperties", extra)
	}
	rawProps, present := schema["properties"]
	if !present {
		return
	}
	props, ok := rawProps.(map[string]any)
	if !ok {
		t.Fatalf("%s schema properties have unexpected type %T", which, rawProps)
	}
	for name, rawProp := range props {
		prop, ok := rawProp.(map[string]any)
		if !ok {
			t.Errorf("%s property %q has unexpected type %T", which, name, rawProp)
			continue
		}
		if d, _ := prop["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("%s property %q has no description", which, name)
		}
		if items, ok := prop["items"].(map[string]any); ok {
			assertPropertiesDescribed(t, which+" "+name+" item", items)
		}
		assertPropertiesDescribed(t, which+" "+name, prop)
	}
}
