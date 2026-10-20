package command

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInProcessTransports_CarryAResultLargerThanAFrame(t *testing.T) {
	ctx := context.Background()
	want := strings.Repeat("x", mcp.DefaultMaxLineLength+1)
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "large", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: want}}}, nil
		})
	serverT, clientT := newInProcessTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "large", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("a result of %d bytes did not arrive: %v", len(want), err)
	}
	if got := resultText(res); got != want {
		t.Errorf("the result arrived as %d bytes, want %d", len(got), len(want))
	}
}

func TestInProcessTransports_ClosingOneEndEndsBoth(t *testing.T) {
	ctx := context.Background()
	serverT, clientT := newInProcessTransports()
	server, _ := serverT.Connect(ctx)
	client, _ := clientT.Connect(ctx)
	read := make(chan error, 1)
	go func() {
		_, err := server.Read(ctx)
		read <- err
	}()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-read:
		if !errors.Is(err, io.EOF) {
			t.Errorf("a read on the other end returned %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the client left the server's read blocked")
	}
	if err := client.Close(); err != nil {
		t.Errorf("a second close returned %v", err)
	}
}
