package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tphakala/jev-mcp/internal/jev"
)

// connect wires NewServer(d) to a fresh client over in-memory transports and
// returns the client session.
func connect(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	srv := NewServer(d)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Errorf("close client session: %v", err)
		}
	})
	return cs
}

// errNoCannedResult is what a fakeEvaluator with neither a result nor an
// error returns, so a call that should never reach the client fails loudly
// instead of handing the handler a nil result.
var errNoCannedResult = errors.New("fakeEvaluator: no canned result")

// fakeEvaluator returns a canned result or error and records the request it
// was given.
type fakeEvaluator struct {
	res *jev.Result
	err error

	mu  sync.Mutex
	got []jev.Request
}

func (f *fakeEvaluator) Evaluate(_ context.Context, req jev.Request) (*jev.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, req)
	if f.res == nil && f.err == nil {
		return nil, errNoCannedResult
	}
	return f.res, f.err
}

func (f *fakeEvaluator) requests() []jev.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// fixtureLatency is the latency resultFrom reports, nonzero so the tests can
// tell it from an unset field.
const fixtureLatency = 1500 * time.Millisecond

// resultFrom decodes a provider response body into a Result, so Answer.Raw is
// populated exactly as the real client populates it.
func resultFrom(t *testing.T, body string) *jev.Result {
	t.Helper()
	var resp jev.Response
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode response fixture: %v", err)
	}
	return &jev.Result{Response: resp, ProviderName: jev.ProviderTypeSafe, Attempts: 1, Latency: fixtureLatency}
}

// callEvaluate calls jev_evaluate with args and returns the result. A protocol
// error fails the test; a tool error is returned in the result for the caller
// to assert.
func callEvaluate(t *testing.T, cs *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: toolEvaluate, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", toolEvaluate, err)
	}
	return res
}

// resultText returns the single text content of a tool result.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("got %d content blocks, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}
