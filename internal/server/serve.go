package server

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"

	"github.com/enola-labs/enola/internal/updatecheck"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Serve runs the server on stdio, as Run does, and accepts sessions on ln as
// well: each connection is its own MCP session on this same server, over the
// one graph it holds, so every process on the machine that asks shares one
// loaded engine instead of loading its own.
//
// Its lifetime is stdio's when stdio carries a session: the caller that
// started it owns it, and when that caller goes the server goes too, closing
// the socket sessions, as a stdio server always has. An engine nobody owns
// would outlive the binary it runs. When stdio carries no session (the server
// was started with its input closed), it serves the socket until ctx ends.
// Either way ln is closed on return, which removes a unix socket's file.
func (s *Server) Serve(ctx context.Context, stdio mcp.Transport, ln net.Listener) error {
	addr := ln.Addr().String()
	s.listening.Store(&addr)
	log.Printf("[server] starting MCP server on stdio transport and on unix socket %s", addr)
	go updatecheck.Refresh(ctx)

	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	var sessions sync.WaitGroup
	acceptDone := make(chan error, 1)
	sessions.Add(1)
	go func() {
		defer sessions.Done()
		acceptDone <- s.accept(serveCtx, ln, &sessions)
	}()
	shutdown := func() {
		stop()
		_ = ln.Close()
		sessions.Wait()
	}

	owner, err := s.mcp.Connect(serveCtx, stdio, nil)
	if err != nil {
		shutdown()
		return err
	}
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- owner.Wait() }()
	closeOwner := func() {
		_ = owner.Close()
		<-ownerDone
	}

	var result error
	select {
	case <-ctx.Done():
		closeOwner()
		result = ctx.Err()
	case err := <-acceptDone:
		closeOwner()
		result = err
	case err := <-ownerDone:
		if owner.InitializeParams() != nil {
			result = err
			break
		}
		select {
		case <-ctx.Done():
			result = ctx.Err()
		case err := <-acceptDone:
			result = err
		}
	}
	shutdown()
	return result
}

// accept connects each accepted connection to the server as a session, until
// ctx ends or ln is closed, and closes the sessions it started when ctx ends.
// A failure to accept that is not the listener closing is retried with a
// growing pause, as net/http does, so a moment out of file descriptors does
// not end a server other callers rely on.
func (s *Server) accept(ctx context.Context, ln net.Listener, sessions *sync.WaitGroup) error {
	var pause time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			pause = min(max(2*pause, 5*time.Millisecond), time.Second)
			log.Printf("[server] accepting on the socket: %v; retrying in %v", err, pause)
			select {
			case <-time.After(pause):
				continue
			case <-ctx.Done():
				return nil
			}
		}
		pause = 0
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			ss, err := s.mcp.Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
			if err != nil {
				_ = conn.Close()
				return
			}
			defer context.AfterFunc(ctx, func() { _ = ss.Close() })()
			_ = ss.Wait()
		}()
	}
}
