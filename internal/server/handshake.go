package server

import (
	"context"
	"fmt"

	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/pkg/status"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Executable names the file this server was started from, as it was then. A
// server compares it with the file on disk when asked, and says in its
// handshake and in every answer's freshness when an install has replaced it.
// Without it a server reads its own binary when it is built.
func Executable(exe status.Executable) Option {
	return func(s *Server) { s.exe, s.exeGiven = exe, true }
}

// handshakeKey is the key under which the initialize result's metadata says
// what this engine holds, beside the version serverInfo already carries.
const handshakeKey = "engine"

// engineHandshake is what a caller needs to tell "reload" from "restart"
// without a second call: which extraction the engine speaks, which snapshot
// and repositories it holds, and whether the binary it runs is still the one
// installed.
type engineHandshake struct {
	ExtractorVersion string          `json:"extractor_version"`
	SnapshotID       string          `json:"snapshot_id,omitempty"`
	Repos            []handshakeRepo `json:"repos"`
	Binary           handshakeBinary `json:"binary"`
	Socket           string          `json:"socket,omitempty"`
}

type handshakeRepo struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	// Receipt is the URI of the repository's receipt resource, the one a
	// caller subscribes to for word of a reload.
	Receipt string `json:"receipt"`
}

type handshakeBinary struct {
	Path     string `json:"path"`
	Replaced bool   `json:"replaced"`
}

func (s *Server) handshake() engineHandshake {
	h := engineHandshake{
		ExtractorVersion: engine.ExtractorVersion(),
		SnapshotID:       s.loadedSnapshotID(),
		Repos:            []handshakeRepo{},
		Binary:           handshakeBinary{Path: s.exe.Path, Replaced: s.exe.Replaced()},
	}
	for _, m := range s.loadedMembers() {
		h.Repos = append(h.Repos, handshakeRepo{Label: m.label, Path: m.path, Receipt: receiptURI(m.label)})
	}
	if p := s.listening.Load(); p != nil {
		h.Socket = *p
	}
	return h
}

// handshakeMiddleware adds the engine's description to the handshake: the
// initialize result, and server/discover's, which takes its place for a client
// on the stateless protocol. It is read when the session starts; a caller that
// holds a session learns of a reload from the receipt resources and of a
// replaced binary from the freshness of any answer.
func (s *Server) handshakeMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if err != nil || result == nil || (method != "initialize" && method != "server/discover") {
			return result, err
		}
		meta := result.GetMeta()
		if meta == nil {
			meta = map[string]any{}
		}
		meta[handshakeKey] = s.handshake()
		result.SetMeta(meta)
		return result, err
	}
}

// loadedMembers is members restricted to what the engine holds: nothing when
// it holds no snapshot, rather than the repositories the config names.
func (s *Server) loadedMembers() []member {
	if s.eng.Snapshot() == nil || s.eng.Store().Count() == 0 {
		return nil
	}
	return s.members()
}

// replacedNotice is what an answer says when the binary this server started
// from has been replaced on disk.
func (s *Server) replacedNotice() string {
	return fmt.Sprintf("⚠️ The binary this server started from (%s) was replaced on disk after it started, and it still answers from the build it loaded. Restart the server to answer from the one installed now.", s.exe.Path)
}
