package server

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// receiptURIPrefix begins the URI of each loaded repository's receipt
// resource; the repository's label ends it.
const receiptURIPrefix = "enola://receipt/"

// receiptCheckInterval is how often the workspace receipt is checked while a
// session is subscribed to a receipt, so a sibling process's generate reaches
// the subscriber without the subscriber making a call. With no subscription
// the server checks only on a call, as it always has.
const receiptCheckInterval = 2 * time.Second

func receiptURI(label string) string { return receiptURIPrefix + url.PathEscape(label) }

// receipts is the bookkeeping behind the receipt resources: which URIs are
// registered on the MCP server, which sessions subscribed to which, the
// snapshot the subscribers were last told about, and whether the watch that
// checks the workspace receipt for them is running.
type receipts struct {
	syncMu sync.Mutex
	listed map[string]bool

	mu        sync.Mutex
	subs      map[*mcp.ServerSession]map[string]bool
	announced string
	watching  bool
}

// syncReceipts registers a receipt resource for each repository the engine
// holds and removes those it no longer holds. The server lists no change of
// the list to its sessions, so a session that did not ask is sent nothing.
func (s *Server) syncReceipts() {
	want := map[string]member{}
	for _, m := range s.loadedMembers() {
		want[receiptURI(m.label)] = m
	}
	rc := &s.receipts
	rc.syncMu.Lock()
	defer rc.syncMu.Unlock()
	var gone []string
	for uri := range rc.listed {
		if _, ok := want[uri]; !ok {
			gone = append(gone, uri)
			delete(rc.listed, uri)
		}
	}
	if len(gone) > 0 {
		s.mcp.RemoveResources(gone...)
	}
	for uri, m := range want {
		if rc.listed[uri] {
			continue
		}
		if rc.listed == nil {
			rc.listed = map[string]bool{}
		}
		rc.listed[uri] = true
		s.mcp.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        m.label,
			Title:       "Receipt of " + m.label,
			Description: "What the loaded snapshot of " + m.path + " was generated over, as snapshot_receipt answers it in full. Subscribe to hear when the snapshot reloads.",
			MIMEType:    "application/json",
		}, s.readReceipt)
	}
}

func (s *Server) receiptListed(uri string) bool {
	s.receipts.syncMu.Lock()
	defer s.receipts.syncMu.Unlock()
	return s.receipts.listed[uri]
}

// readReceipt answers a receipt resource with the receipt the engine holds for
// that repository now, as snapshot_receipt's full answer encodes it.
func (s *Server) readReceipt(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	snap := s.eng.Snapshot()
	for _, m := range s.loadedMembers() {
		if receiptURI(m.label) != uri || snap == nil {
			continue
		}
		receipt := snap.Meta.Receipt()
		if len(s.eng.RepoPaths()) > 1 {
			receipt = s.eng.MetaFor(m.path).Receipt()
		}
		data, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}}}, nil
	}
	return nil, mcp.ResourceNotFoundError(uri)
}

// subscribe accepts a subscription to a loaded repository's receipt and makes
// sure the workspace receipt is being watched for it.
func (s *Server) subscribe(_ context.Context, req *mcp.SubscribeRequest) error {
	s.syncReceipts()
	uri := req.Params.URI
	if !s.receiptListed(uri) {
		return mcp.ResourceNotFoundError(uri)
	}
	rc := &s.receipts
	rc.mu.Lock()
	if len(rc.subs) == 0 {
		rc.subs = map[*mcp.ServerSession]map[string]bool{}
		rc.announced = s.loadedSnapshotID()
	}
	if rc.subs[req.Session] == nil {
		rc.subs[req.Session] = map[string]bool{}
	}
	rc.subs[req.Session][uri] = true
	start := !rc.watching
	rc.watching = true
	rc.mu.Unlock()
	if start {
		go s.watchReceipts()
	}
	return nil
}

func (s *Server) unsubscribe(_ context.Context, req *mcp.UnsubscribeRequest) error {
	rc := &s.receipts
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if uris := rc.subs[req.Session]; uris != nil {
		delete(uris, req.Params.URI)
		if len(uris) == 0 {
			delete(rc.subs, req.Session)
		}
	}
	return nil
}

// watchReceipts checks the workspace receipt while any session is subscribed,
// reloading when a sibling rewrote it, and stops when the last subscriber has
// gone. A session that ends without unsubscribing is dropped here, since the
// SDK forgets its subscriptions without telling the server.
func (s *Server) watchReceipts() {
	tick := time.NewTicker(receiptCheckInterval)
	defer tick.Stop()
	for range tick.C {
		if !s.keepWatching() {
			return
		}
		s.reloadIfRewritten()
		s.announce()
	}
}

func (s *Server) keepWatching() bool {
	live := map[*mcp.ServerSession]bool{}
	for ss := range s.mcp.Sessions() {
		live[ss] = true
	}
	rc := &s.receipts
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for ss := range rc.subs {
		if !live[ss] {
			delete(rc.subs, ss)
		}
	}
	if len(rc.subs) == 0 {
		rc.watching = false
		return false
	}
	return true
}

// announce tells every subscriber that the snapshot it holds reloaded, once
// per snapshot the engine comes to serve. A union reloads as one, so every
// subscribed receipt is told, and a repository that left the union is told
// too: reading it now answers that it is not loaded.
func (s *Server) announce() {
	id := s.loadedSnapshotID()
	rc := &s.receipts
	rc.mu.Lock()
	if len(rc.subs) == 0 || id == rc.announced {
		rc.mu.Unlock()
		return
	}
	rc.announced = id
	subscribed := map[string]bool{}
	for _, uris := range rc.subs {
		for uri := range uris {
			subscribed[uri] = true
		}
	}
	rc.mu.Unlock()

	s.syncReceipts()
	uris := make([]string, 0, len(subscribed))
	for uri := range subscribed {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		_ = s.mcp.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: uri})
	}
}

// receiptsMiddleware keeps the receipt resources in step with what the engine
// holds before a caller lists or reads them, and after every tool call tells
// subscribers of a reload the call brought about: a generate through any
// session, or a sibling's rewrite the call noticed.
func (s *Server) receiptsMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "resources/list", "resources/read":
			s.syncReceipts()
		}
		result, err := next(ctx, method, req)
		if method == "tools/call" {
			s.announce()
		}
		return result, err
	}
}
