package server

import (
	"context"
	"testing"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A client on the stateless protocol opens with server/discover rather than
// initialize, so the engine's description rides that result as well. The
// SDK's own client does not keep that result's metadata, which is why this is
// checked at the middleware rather than through a client.
func TestHandshakeRidesServerDiscover(t *testing.T) {
	eng, err := engine.New(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{eng: eng, cfg: config.Default()}
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) { return &mcp.DiscoverResult{}, nil }
	for _, method := range []string{"server/discover", "initialize"} {
		res, err := s.handshakeMiddleware(next)(context.Background(), method, nil)
		if err != nil {
			t.Fatal(err)
		}
		h, ok := res.GetMeta()[handshakeKey].(engineHandshake)
		if !ok {
			t.Fatalf("%s: no engine description in the result's metadata: %+v", method, res.GetMeta())
		}
		if h.ExtractorVersion != engine.ExtractorVersion() || h.Repos == nil {
			t.Errorf("%s: description = %+v", method, h)
		}
	}
	res, err := s.handshakeMiddleware(next)(context.Background(), "tools/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.GetMeta()[handshakeKey]; ok {
		t.Error("a result other than the handshake carries the engine's description")
	}
}
