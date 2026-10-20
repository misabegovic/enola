package command

import (
	"context"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newInProcessTransports returns a connected server and client transport that
// hand each other whole messages, for a command answering one tool call in its
// own process.
//
// mcp.NewInMemoryTransports frames messages over a net.Pipe, and its reader
// refuses any frame above mcp.DefaultMaxLineLength (16 MiB) with no way to
// raise it. That bound guards a server against a peer it does not trust; here
// both ends are this process, and the frame is the tool's own answer, which
// grows with the repository: a check over a large union with hundreds of
// breaches exceeded it and graded nothing. Each message is still encoded and
// decoded once, so the client reads exactly what a wire would carry.
func newInProcessTransports() (mcp.Transport, mcp.Transport) {
	p := &messagePipe{done: make(chan struct{})}
	toServer, toClient := make(chan jsonrpc.Message), make(chan jsonrpc.Message)
	return &inProcessTransport{&inProcessConn{p, toServer, toClient}},
		&inProcessTransport{&inProcessConn{p, toClient, toServer}}
}

type inProcessTransport struct{ conn *inProcessConn }

func (t *inProcessTransport) Connect(context.Context) (mcp.Connection, error) {
	return t.conn, nil
}

// messagePipe is what both ends share: closing either closes both, as a
// net.Pipe does.
type messagePipe struct {
	once sync.Once
	done chan struct{}
}

type inProcessConn struct {
	pipe *messagePipe
	in   <-chan jsonrpc.Message
	out  chan<- jsonrpc.Message
}

func (c *inProcessConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case msg := <-c.in:
		return msg, nil
	case <-c.pipe.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *inProcessConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}
	copied, err := jsonrpc.DecodeMessage(data)
	if err != nil {
		return err
	}
	select {
	case c.out <- copied:
		return nil
	case <-c.pipe.done:
		return mcp.ErrConnectionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *inProcessConn) Close() error {
	c.pipe.once.Do(func() { close(c.pipe.done) })
	return nil
}

func (c *inProcessConn) SessionID() string { return "" }
