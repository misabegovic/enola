package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/internal/wireschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addTypedTool registers a tool that answers with a value a program reads as
// well as the text it has always returned. The tool lists the schema of that
// value as its output schema and every answer that is not an error carries the
// value as its structured content, whole: the token cap cuts the text, which a
// reader reads, and never the value.
//
// The schema is wireschema's reading of Out, the same one `contract` prints for
// the documents the command line writes, so a tool and the verb it shares an
// answer with state one shape. The value is encoded here, in Out's own field
// order, rather than by the SDK, which derives its schema by other rules and
// re-encodes an object's fields in key order after checking it. The text is
// the handler's own, untouched.
func addTypedTool[In, Out any](s *Server, t *mcp.Tool, h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, *Out, error)) {
	typed := *t
	typed.OutputSchema = outputSchema(reflect.TypeFor[Out](), t.Name)
	addTool(s, &typed, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		res, out, err := h(ctx, req, in)
		if err != nil || res == nil || res.IsError {
			return res, nil, err
		}
		if out == nil {
			out = new(Out)
		}
		raw, err := json.Marshal(diff.WithIdentities(*out))
		if err != nil {
			return errorResult(fmt.Sprintf("failed to marshal results: %v", err)), nil, nil
		}
		res.StructuredContent = json.RawMessage(raw)
		return res, nil, nil
	})
}

// freshness is the staleness banner as a field of a typed answer: present only
// when the banner is, and saying what the banner says. The text keeps its
// banner; a program reads this instead of parsing it.
type freshness struct {
	Stale bool `json:"stale"`
	// Notice is the banner's sentence, as the text carries it.
	Notice string `json:"notice"`
	// GeneratedAt is when the loaded graph was generated, when known.
	GeneratedAt string `json:"generated_at,omitempty"`
	// TooOld says the graph is older than the day the banner allows.
	TooOld bool `json:"too_old"`
	// Age is the graph's age as the banner rounds it.
	Age string `json:"age,omitempty"`
	// Changed lists each repository whose code moved since its snapshot.
	Changed []staleRepo `json:"changed,omitempty"`
	// BinaryReplaced says the file this server was started from has been
	// replaced on disk since, so the answer comes from a build no longer
	// installed.
	BinaryReplaced bool `json:"binary_replaced,omitempty"`
}

type staleRepo struct {
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

// freshnessField is the key the freshness rides under in every typed answer.
const freshnessField = "freshness"

// outputSchema is the schema a typed tool lists: what Out encodes as, with the
// freshness field every typed answer may carry.
func outputSchema(t reflect.Type, title string) map[string]any {
	schema, err := wireschema.For(t, title)
	if err != nil {
		panic(fmt.Sprintf("addTypedTool: tool %q: %v", title, err))
	}
	if schema["type"] != "object" {
		panic(fmt.Sprintf("addTypedTool: tool %q answers with %v, and structured content is an object", title, schema["type"]))
	}
	fresh, err := wireschema.For(reflect.TypeFor[freshness](), "")
	if err != nil {
		panic(err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, taken := props[freshnessField]; taken {
		panic(fmt.Sprintf("addTypedTool: tool %q already answers with a field named %s", title, freshnessField))
	}
	field := map[string]any{"type": "object", "properties": fresh["properties"], "required": fresh["required"]}
	props[freshnessField] = field
	if defs, ok := fresh["$defs"].(map[string]any); ok {
		all, _ := schema["$defs"].(map[string]any)
		if all == nil {
			all = map[string]any{}
			schema["$defs"] = all
		}
		for name, def := range defs {
			all[name] = def
		}
	}
	return schema
}

// withFreshness adds the freshness field to a result's structured content, when
// it has an object for one. The field goes last, so the answer's own fields
// keep the order its type gives them.
func withFreshness(ctr *mcp.CallToolResult, f freshness) {
	raw, ok := ctr.StructuredContent.(json.RawMessage)
	if !ok {
		return
	}
	body := bytes.TrimSpace(raw)
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		return
	}
	var out bytes.Buffer
	out.Write(body[:len(body)-1])
	if len(bytes.TrimSpace(body[1:len(body)-1])) > 0 {
		out.WriteByte(',')
	}
	out.WriteString(`"` + freshnessField + `":`)
	out.Write(encoded)
	out.WriteByte('}')
	ctr.StructuredContent = json.RawMessage(out.Bytes())
}

// jsonTyped is jsonResultCapped for a typed tool: the value's JSON as the text,
// cut at maxTokens, and the value itself as the answer.
func jsonTyped[T any](v T, maxTokens int) (*mcp.CallToolResult, *T, error) {
	res, _, err := jsonResultCapped(v, maxTokens)
	return res, &v, err
}

// jsonTypedWhole is jsonResult for a typed tool.
func jsonTypedWhole[T any](v T) (*mcp.CallToolResult, *T, error) {
	res, _, err := jsonResult(v)
	return res, &v, err
}
