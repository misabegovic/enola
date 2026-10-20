package server_test

// One loaded engine serving every process on the machine that asks it: a unix
// socket beside stdio, a handshake that says what the engine holds and which
// binary it runs, receipt resources a caller subscribes to for reload notices,
// and progress on the long calls. Each test drives the server through the
// SDK's own client, as any caller would.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/pkg/bootstrap"
	"github.com/enola-labs/enola/pkg/status"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// socketPath is a socket path short enough for the platform's limit, which a
// test's temporary directory can exceed.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "enola-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "e.sock")
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// closedStdio is standard input at end of file before any message, the shape
// of a server started with its input closed: no session ever arrives on it.
func closedStdio() mcp.Transport {
	return &mcp.IOTransport{Reader: io.NopCloser(strings.NewReader("")), Writer: nopWriteCloser{io.Discard}}
}

// dialSession connects an MCP client to the server behind a unix socket.
func dialSession(t *testing.T, path string, opts *mcp.ClientOptions) *session {
	t.Helper()
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if conn, err = net.Dial("unix", path); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "enola-test", Version: "0"}, opts).
		Connect(context.Background(), &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
	if err != nil {
		t.Fatalf("client over the socket: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &session{cs: cs}
}

// serveOnSocket starts srv on stdio and on a fresh socket, and returns the
// socket's path, a cancel for the server's context and what Serve returned.
func serveOnSocket(t *testing.T, srv *bootstrap.Server, stdio mcp.Transport) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- srv.Serve(ctx, stdio, ln)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after its context was cancelled")
		}
	})
	return path, cancel, done
}

func newServer(t *testing.T, eng *bootstrap.Engine, cfg *config.Config, opts ...bootstrap.ServerOption) *bootstrap.Server {
	t.Helper()
	srv, err := bootstrap.NewServer(eng, cfg, opts...)
	if err != nil {
		t.Fatalf("bootstrap.NewServer: %v", err)
	}
	return srv
}

// attach connects one more in-memory client to an existing server.
func attach(t *testing.T, srv *bootstrap.Server, opts *mcp.ClientOptions) *session {
	t.Helper()
	return attachSpeaking(t, srv, opts, "")
}

// legacyProtocol is the protocol of a client that opens with initialize: the
// eval scripts, and most clients in use. The SDK's own client speaks the
// stateless protocol unless told otherwise, and its handshake is
// server/discover, whose metadata that client does not keep.
const legacyProtocol = "2025-11-25"

// attachSpeaking is attach for a client that speaks the given protocol
// version, or the SDK's latest when it is empty.
func attachSpeaking(t *testing.T, srv *bootstrap.Server, opts *mcp.ClientOptions, protocol string) *session {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "enola-test", Version: "0"}, opts).Connect(ctx, clientT, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &session{cs: cs}
}

func goSample(t *testing.T) string {
	t.Helper()
	return copyTree(t, filepath.Join("..", "engine", "testdata", "repos", "go_sample"), t.TempDir())
}

// Two clients on one socket are two sessions on one server over one loaded
// graph: what one generates, the other answers from without generating.
func TestE2E_SocketSessionsShareOneEngine(t *testing.T) {
	eng, cfg := newTestEngine(t)
	srv := newServer(t, eng, cfg)
	path, cancel, done := serveOnSocket(t, srv, closedStdio())

	a := dialSession(t, path, nil)
	b := dialSession(t, path, nil)
	a.repo = goSample(t)
	a.snapshot(t)

	got := text(b.call(t, "query_facts", map[string]any{"kind": "module", "output_mode": "names"}))
	if !strings.Contains(got, "pkg/a") {
		t.Fatalf("a second client does not see the graph the first generated:\n%s", got)
	}
	ra := text(a.call(t, "snapshot_receipt", map[string]any{"output_mode": "full"}))
	rb := text(b.call(t, "snapshot_receipt", map[string]any{"output_mode": "full"}))
	if ra != rb {
		t.Errorf("the two sessions answer from different snapshots:\n%s\n---\n%s", ra, rb)
	}

	var wg sync.WaitGroup
	for _, s := range []*session{a, b} {
		wg.Add(1)
		go func(s *session) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if res, err := s.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "impact_analysis", Arguments: map[string]any{"target": "pkg/a.Alpha"}}); err != nil || res.IsError {
					t.Errorf("concurrent impact_analysis: %v", err)
					return
				}
			}
		}(s)
	}
	wg.Wait()

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Serve after its context was cancelled returned %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the server: %v", err)
	}
}

// A server whose standard input carried a session belongs to that caller:
// when the caller goes, the server goes with it, socket sessions included, as
// a stdio server always has. An engine nobody owns would outlive its binary.
func TestE2E_TheStdioOwnerLeavingEndsTheServer(t *testing.T) {
	eng, cfg := newTestEngine(t)
	srv := newServer(t, eng, cfg)
	ownerServer, ownerClient := mcp.NewInMemoryTransports()
	path, _, done := serveOnSocket(t, srv, ownerServer)

	owner, err := mcp.NewClient(&mcp.Implementation{Name: "owner", Version: "0"}, nil).Connect(context.Background(), ownerClient, nil)
	if err != nil {
		t.Fatalf("owner over stdio: %v", err)
	}
	guest := dialSession(t, path, nil)
	if _, err := guest.cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("a guest on the socket: %v", err)
	}

	_ = owner.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after its owner left returned %v, want nil as a stdio server does", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server kept running after the caller that owned it left")
	}
	if _, err := guest.cs.ListTools(context.Background(), nil); err == nil {
		t.Error("a socket session still answers after the server ended")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the server: %v", err)
	}
}

type engineMeta struct {
	ExtractorVersion string `json:"extractor_version"`
	SnapshotID       string `json:"snapshot_id"`
	Repos            []struct {
		Label   string `json:"label"`
		Path    string `json:"path"`
		Receipt string `json:"receipt"`
	} `json:"repos"`
	Binary struct {
		Path     string `json:"path"`
		Replaced bool   `json:"replaced"`
	} `json:"binary"`
	Socket string `json:"socket"`
}

func handshake(t *testing.T, s *session) engineMeta {
	t.Helper()
	init := s.cs.InitializeResult()
	if init == nil {
		t.Fatal("no initialize result")
	}
	raw, ok := init.Meta["engine"]
	if !ok {
		t.Fatalf("the initialize result carries no engine metadata: %+v", init.Meta)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m engineMeta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("engine metadata %s: %v", data, err)
	}
	return m
}

func testBinary(t *testing.T) (string, status.Executable) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "enola")
	if err := os.WriteFile(bin, []byte("build one"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := status.ReadExecutable(bin)
	if err != nil {
		t.Fatal(err)
	}
	return bin, exe
}

func replaceBinary(t *testing.T, bin string) {
	t.Helper()
	next := bin + ".new"
	if err := os.WriteFile(next, []byte("build two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}
}

// The handshake says what the engine holds and which binary it runs, so a
// caller can tell "restart" from "reload" without a second call.
func TestE2E_HandshakeSaysWhatTheEngineHolds(t *testing.T) {
	eng, cfg := newTestEngine(t)
	bin, exe := testBinary(t)
	srv := newServer(t, eng, cfg, bootstrap.Executable(exe))

	empty := handshake(t, attachSpeaking(t, srv, nil, legacyProtocol))
	if empty.ExtractorVersion != engine.ExtractorVersion() {
		t.Errorf("extractor_version = %q, want %q", empty.ExtractorVersion, engine.ExtractorVersion())
	}
	if empty.SnapshotID != "" || len(empty.Repos) != 0 {
		t.Errorf("an engine holding nothing names snapshot %q and repos %+v", empty.SnapshotID, empty.Repos)
	}
	if empty.Binary.Path != bin || empty.Binary.Replaced {
		t.Errorf("binary = %+v, want %q not replaced", empty.Binary, bin)
	}

	first := attach(t, srv, nil)
	first.repo = goSample(t)
	first.snapshot(t)
	held := handshake(t, attachSpeaking(t, srv, nil, legacyProtocol))
	if held.SnapshotID == "" || held.SnapshotID != eng.Snapshot().Meta.SnapshotID {
		t.Errorf("snapshot_id = %q, want the loaded %q", held.SnapshotID, eng.Snapshot().Meta.SnapshotID)
	}
	abs, _ := filepath.Abs(first.repo)
	if len(held.Repos) != 1 || held.Repos[0].Path != abs || held.Repos[0].Label == "" || held.Repos[0].Receipt == "" {
		t.Fatalf("repos = %+v, want the one loaded repository at %s with its receipt", held.Repos, abs)
	}

	// The replaced flag is read when it is asked for, so a caller that was
	// told "not replaced" hears otherwise on its next answer.
	replaceBinary(t, bin)
	if again := handshake(t, attachSpeaking(t, srv, nil, legacyProtocol)); !again.Binary.Replaced {
		t.Error("after the binary was replaced the handshake still says it was not")
	}
	res := first.call(t, "query_facts", map[string]any{"kind": "module"})
	if res.IsError {
		t.Fatalf("query_facts: %s", text(res))
	}
	if !strings.Contains(text(res), "replaced") {
		t.Errorf("the answer's banner does not say the binary was replaced:\n%.400s", text(res))
	}
	var answer struct {
		Freshness *struct {
			Stale          bool `json:"stale"`
			BinaryReplaced bool `json:"binary_replaced"`
		} `json:"freshness"`
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Freshness == nil || !answer.Freshness.BinaryReplaced || !answer.Freshness.Stale {
		t.Errorf("the typed answer's freshness does not say the binary was replaced: %s", raw)
	}
	lint := first.call(t, "constraints_lint", map[string]any{"format": "json"})
	if lint.IsError {
		t.Fatalf("constraints_lint: %s", text(lint))
	}
	raw, _ = json.Marshal(lint.StructuredContent)
	if !strings.Contains(string(raw), `"binary_replaced":true`) {
		t.Errorf("a tool that states its own currency does not carry the replaced flag in its structured answer: %.300s", raw)
	}
}

// A subscriber to a repository's receipt hears that the snapshot reloaded
// within seconds of a sibling process rewriting it, without making a call;
// a session that did not subscribe hears nothing.
func TestE2E_ReceiptSubscribersHearASiblingGenerate(t *testing.T) {
	ctx := context.Background()
	repo := goSample(t)

	writer, writerCfg := newTestEngine(t)
	writerCfg.Repo = repo
	generateAndWrite := func() string {
		t.Helper()
		if _, err := writer.GenerateSnapshot(ctx, repo, false); err != nil {
			t.Fatalf("writer GenerateSnapshot: %v", err)
		}
		if err := writer.WriteArtifacts(repo); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteGlobalReceipt(); err != nil {
			t.Fatal(err)
		}
		gr, err := engine.LoadWorkspaceReceipt(repo)
		if err != nil {
			t.Fatal(err)
		}
		return gr.SnapshotID
	}
	firstID := generateAndWrite()

	served, servedCfg := newTestEngine(t)
	servedCfg.Repo = repo
	srv := newServer(t, served, servedCfg)

	updates := make(chan string, 8)
	subscriber := attachSpeaking(t, srv, &mcp.ClientOptions{ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
		updates <- req.Params.URI
	}}, legacyProtocol)
	statelessUpdates := make(chan string, 8)
	stateless := attach(t, srv, &mcp.ClientOptions{ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
		statelessUpdates <- req.Params.URI
	}})
	var bystanderHeard sync.Mutex
	heard := 0
	bystander := attach(t, srv, &mcp.ClientOptions{ResourceUpdatedHandler: func(context.Context, *mcp.ResourceUpdatedNotificationRequest) {
		bystanderHeard.Lock()
		heard++
		bystanderHeard.Unlock()
	}})

	subscriber.call(t, "query_facts", map[string]any{"kind": "module"})
	list, err := subscriber.cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	if len(list.Resources) != 1 {
		t.Fatalf("resources/list = %d resources, want the one loaded repository's receipt", len(list.Resources))
	}
	uri := list.Resources[0].URI
	read, err := subscriber.cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(read.Contents) != 1 {
		t.Fatalf("resources/read %s: %v", uri, err)
	}
	if !strings.Contains(read.Contents[0].Text, firstID) {
		t.Errorf("the receipt resource does not name the loaded snapshot %s:\n%.300s", firstID, read.Contents[0].Text)
	}
	if err := subscriber.cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("resources/subscribe: %v", err)
	}
	if err := subscriber.cs.Subscribe(ctx, &mcp.SubscribeParams{URI: "enola://receipt/no-such-repository"}); err == nil {
		t.Error("a subscription to a receipt no loaded repository has was accepted")
	}
	if err := stateless.cs.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatalf("subscribing on the stateless protocol: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(repo, "pkg")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	secondID := generateAndWrite()
	if secondID == firstID {
		t.Fatal("the rewrite did not move the snapshot id")
	}
	select {
	case got := <-updates:
		if got != uri {
			t.Errorf("resources/updated for %q, want %q", got, uri)
		}
		t.Logf("resources/updated %s after the sibling's write", time.Since(start).Round(time.Millisecond))
	case <-time.After(3 * time.Second):
		t.Fatal("no resources/updated within 3 s of a sibling generate")
	}
	select {
	case got := <-statelessUpdates:
		if got != uri {
			t.Errorf("stateless resources/updated for %q, want %q", got, uri)
		}
	case <-time.After(time.Second):
		t.Error("a subscriber on the stateless protocol heard no resources/updated")
	}
	if id := served.Snapshot().Meta.SnapshotID; id != secondID {
		t.Errorf("after the notice the server serves %q, disk holds %q", id, secondID)
	}
	if res := bystander.call(t, "snapshot_receipt", map[string]any{"output_mode": "full"}); !strings.Contains(text(res), secondID) {
		t.Errorf("the next call does not read the new snapshot %s", secondID)
	}
	bystanderHeard.Lock()
	defer bystanderHeard.Unlock()
	if heard != 0 {
		t.Errorf("a session that subscribed to nothing heard %d resource updates", heard)
	}
}

type progressLog struct {
	mu    sync.Mutex
	notes []*mcp.ProgressNotificationParams
}

func (p *progressLog) handler(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notes = append(p.notes, req.Params)
}

func (p *progressLog) seen() []*mcp.ProgressNotificationParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*mcp.ProgressNotificationParams(nil), p.notes...)
}

func callWithToken(t *testing.T, s *session, token any, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	if token != nil {
		params.SetProgressToken(token)
	}
	res, err := s.cs.CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: %s", name, text(res))
	}
	return res
}

func checkProgress(t *testing.T, notes []*mcp.ProgressNotificationParams, token any) {
	t.Helper()
	if len(notes) == 0 {
		t.Fatal("no progress notification before the answer")
	}
	last := -1.0
	for _, n := range notes {
		if n.ProgressToken != token {
			t.Errorf("progress token %v, want %v", n.ProgressToken, token)
		}
		if n.Progress <= last {
			t.Errorf("progress %v after %v: it must increase with each notification", n.Progress, last)
		}
		if n.Total <= 0 || n.Progress > n.Total {
			t.Errorf("progress %v of %v: files extracted of files found", n.Progress, n.Total)
		}
		if !strings.Contains(n.Message, "files") {
			t.Errorf("progress message %q does not say what is counted", n.Message)
		}
		last = n.Progress
	}
}

// generate_snapshot with a progress token hears how far the extraction has
// got; without one it hears nothing, as before.
func TestE2E_GenerateReportsProgressToATokenedRequest(t *testing.T) {
	eng, cfg := newTestEngine(t)
	srv := newServer(t, eng, cfg)
	var log progressLog
	s := attach(t, srv, &mcp.ClientOptions{ProgressNotificationHandler: log.handler})
	repo := goSample(t)

	callWithToken(t, s, nil, "generate_snapshot", map[string]any{"repo_path": repo})
	if n := len(log.seen()); n != 0 {
		t.Fatalf("a generate that carried no progress token was sent %d progress notifications", n)
	}
	callWithToken(t, s, "gen-1", "generate_snapshot", map[string]any{"repo_path": repo, "fresh": true})
	checkProgress(t, log.seen(), "gen-1")
}

// A grade whose tree moved regenerates in memory, and a token on it hears
// that regenerate's progress the way a generate's does.
func TestE2E_RegeneratingGradeReportsProgress(t *testing.T) {
	alpha := writeRepo(t, "alpha", map[string]string{
		"go.mod":                  "module alpha\n\ngo 1.21\n",
		"internal/store/store.go": "package store\n\nfunc Put() {}\n",
		"internal/api/api.go":     "package api\n\nimport \"alpha/internal/store\"\n\nfunc Serve() { store.Put() }\n",
		"enola-intent.yaml":       alphaLaw,
	})
	eng, cfg := newTestEngine(t)
	srv := newServer(t, eng, cfg)
	var log progressLog
	s := attach(t, srv, &mcp.ClientOptions{ProgressNotificationHandler: log.handler})
	s.repo = alpha
	s.snapshot(t)
	if res := s.call(t, "set_baseline", map[string]any{}); res.IsError {
		t.Fatalf("set_baseline: %s", text(res))
	}
	api := filepath.Join(alpha, "internal", "api", "api.go")
	if err := os.WriteFile(api, []byte("package api\n\nimport \"alpha/internal/store\"\n\nfunc Serve() { store.Put() }\n\nfunc Again() { store.Put() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(log.seen())
	callWithToken(t, s, 7, "grade", map[string]any{"format": "json"})
	checkProgress(t, log.seen()[before:], float64(7))
}

// With no socket, no subscription, no token and the binary it started from,
// a session hears nothing it did not hear before: no notification of any
// kind, and no answer carries the new flag.
func TestE2E_NothingAskedMeansNothingNew(t *testing.T) {
	for name, protocol := range map[string]string{"initialize": legacyProtocol, "stateless": ""} {
		t.Run(name, func(t *testing.T) { nothingAskedMeansNothingNew(t, protocol) })
	}
}

func nothingAskedMeansNothingNew(t *testing.T, protocol string) {
	eng, cfg := newTestEngine(t)
	_, exe := testBinary(t)
	srv := newServer(t, eng, cfg, bootstrap.Executable(exe))
	var mu sync.Mutex
	notices := 0
	count := func() { mu.Lock(); notices++; mu.Unlock() }
	s := attachSpeaking(t, srv, &mcp.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *mcp.ProgressNotificationClientRequest) { count() },
		ResourceUpdatedHandler:      func(context.Context, *mcp.ResourceUpdatedNotificationRequest) { count() },
		ResourceListChangedHandler:  func(context.Context, *mcp.ResourceListChangedRequest) { count() },
		ToolListChangedHandler:      func(context.Context, *mcp.ToolListChangedRequest) { count() },
		LoggingMessageHandler:       func(context.Context, *mcp.LoggingMessageRequest) { count() },
		PromptListChangedHandler:    func(context.Context, *mcp.PromptListChangedRequest) { count() },
	}, protocol)
	s.repo = goSample(t)
	s.snapshot(t)
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"query_facts", map[string]any{"kind": "module"}},
		{"snapshot_receipt", map[string]any{"output_mode": "full"}},
		{"impact_analysis", map[string]any{"target": "pkg/a.Alpha"}},
		{"generate_snapshot", map[string]any{"repo_path": s.repo}},
	} {
		res := s.call(t, call.tool, call.args)
		raw, _ := json.Marshal(res.StructuredContent)
		if strings.Contains(string(raw), "binary_replaced") || strings.Contains(text(res), "replaced on disk") {
			t.Errorf("%s speaks of a replaced binary when none was replaced", call.tool)
		}
	}
	// The receipt list follows what the engine holds, and a change of it is
	// not announced: listing, moving to another repository and listing again
	// sends nothing.
	if _, err := s.cs.ListResources(context.Background(), nil); err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	other := writeRepo(t, "other", map[string]string{"go.mod": "module other\n\ngo 1.21\n", "main.go": "package main\n\nfunc main() {}\n"})
	if res := s.call(t, "generate_snapshot", map[string]any{"repo_path": other, "fresh": true}); res.IsError {
		t.Fatalf("generate_snapshot: %s", text(res))
	}
	list, err := s.cs.ListResources(context.Background(), nil)
	if err != nil || len(list.Resources) != 1 || list.Resources[0].Name != "other" {
		t.Fatalf("after moving to another repository the receipts are %+v (%v)", list, err)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if notices != 0 {
		t.Errorf("a session that asked for nothing was sent %d notifications", notices)
	}
	caps := s.cs.InitializeResult().Capabilities
	if caps.Tools == nil || !caps.Tools.ListChanged || caps.Logging == nil {
		t.Errorf("the tools and logging capabilities moved: %+v", caps)
	}
}
