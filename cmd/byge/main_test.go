package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestVersionHelpUnknown(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, env(nil), &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "byge dev") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	// The name, not only the word: the help is the one place the program says
	// what to type, and a rename that misses it leaves the board telling people
	// to run a command that no longer exists. This one said "usage: kanban"
	// from the rename until 2.19.0, because the test asked for "usage:" alone.
	if code := run(context.Background(), []string{"--help"}, env(nil), &out, &errOut); code != 0 || !strings.Contains(out.String(), "usage: byge ") {
		t.Fatalf("help: %d %q", code, out.String())
	}
	if code := run(context.Background(), []string{"frobnicate"}, env(nil), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("unknown: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"serve"}, env(map[string]string{"STORAGE": "oracle"}), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "STORAGE") {
		t.Fatalf("bad config: %d %q", code, errOut.String())
	}
}

func TestMigrateMemory(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"migrate"}, env(map[string]string{"STORAGE": "memory"}), &out, &errOut); code != 0 {
		t.Fatalf("migrate: %d %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "schema up to date") {
		t.Fatalf("log: %s", errOut.String())
	}
}

func TestPostgresUnreachable(t *testing.T) {
	e := env(map[string]string{"DATABASE_URL": "postgres://nobody:none@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"})
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"migrate"}, e, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "migrate") {
		t.Fatalf("migrate: %d %s", code, errOut.String())
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"serve"}, e, &out, &errOut); code != 1 {
		t.Fatalf("serve: %d %s", code, errOut.String())
	}
	// Without auto-migrate, serve fails at the default-board step instead.
	errOut.Reset()
	e2 := env(map[string]string{"DATABASE_URL": "postgres://nobody:none@127.0.0.1:1/none?sslmode=disable&connect_timeout=1", "AUTO_MIGRATE": "false"})
	if code := run(context.Background(), []string{"serve"}, e2, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "prepare default board") {
		t.Fatalf("serve no-migrate: %d %s", code, errOut.String())
	}
}

func TestServeMemory(t *testing.T) {
	addrs := make(chan net.Addr, 1)
	old := notifyListening
	notifyListening = func(a net.Addr) { addrs <- a }
	defer func() { notifyListening = old }()

	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuf{}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, nil, env(map[string]string{"STORAGE": "memory", "LISTEN_ADDR": "127.0.0.1:0", "LOG_FORMAT": "json"}), io.Discard, logs)
	}()
	var addr net.Addr
	select {
	case addr = <-addrs:
	case <-time.After(5 * time.Second):
		t.Fatalf("server did not start: %s", logs.String())
	}
	base := "http://" + addr.String()
	// AUTH_MODE defaults to none, so this request is anonymous. Under stage-3
	// enforcement an anonymous caller holds no grant on the bootstrap default
	// board, so the front page lists the boards it may read, which is none,
	// rather than redirecting into a board it would then be refused. The page
	// is still served, not a 403: the list filters, it does not refuse.
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "No boards yet") || resp.Request.URL.Path != "/" {
		t.Fatalf("GET /: %d %s %s", resp.StatusCode, resp.Request.URL, body)
	}
	resp, err = http.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d", resp.StatusCode)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d: %s", code, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop")
	}
	if !strings.Contains(logs.String(), `"msg":"shutting down"`) {
		t.Fatalf("logs: %s", logs.String())
	}
}

// TestServeSQLite is the SQLite wiring end to end: a board created over HTTP
// is still there after the process that created it has exited.
//
// It runs in proxy mode with an owner address on every request. Stage-3
// enforcement refuses an anonymous caller, so creating a board needs somebody
// to own it and reading one needs a grant; the proxy header is how an
// identity reaches the service in a deployment that has one. The address that
// creates the board becomes its owner, which is the grant the later read
// relies on, including after the restart that is the point of this test.
func TestServeSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	e := env(map[string]string{
		"STORAGE":     "sqlite",
		"SQLITE_PATH": path,
		"LISTEN_ADDR": "127.0.0.1:0",
		"AUTH_MODE":   "proxy",
		"AUTH_HEADER": "X-Forwarded-Email",
	})
	const owner = "owner@example.com"
	addrs := make(chan net.Addr, 1)
	old := notifyListening
	notifyListening = func(a net.Addr) { addrs <- a }
	defer func() { notifyListening = old }()

	start := func() (string, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		logs := &syncBuf{}
		done := make(chan int, 1)
		go func() { done <- run(ctx, nil, e, io.Discard, logs) }()
		var addr net.Addr
		select {
		case addr = <-addrs:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatalf("server did not start: %s", logs.String())
		}
		return "http://" + addr.String(), func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("exit code %d: %s", code, logs.String())
				}
			case <-time.After(15 * time.Second):
				t.Error("server did not stop")
			}
		}
	}

	// do sends a request as the owner, so the proxy header names a caller the
	// service will grant a role.
	do := func(req *http.Request) *http.Response {
		req.Header.Set("X-Forwarded-Email", owner)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	base, stop := start()
	req, err := http.NewRequest(http.MethodPost, base+"/boards",
		strings.NewReader(url.Values{"name": {"Homelab"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/b/homelab" {
		t.Fatalf("POST /boards: %d %s", resp.StatusCode, resp.Request.URL)
	}
	if !strings.Contains(string(body), "Homelab") {
		t.Fatalf("board page does not name the board: %s", body)
	}
	stop()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file: %v", err)
	}

	base, stop = start()
	defer stop()
	req, err = http.NewRequest(http.MethodGet, base+"/b/homelab", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp = do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Homelab") {
		t.Fatalf("GET /b/homelab after restart: %d %s", resp.StatusCode, body)
	}
}

func TestListenFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var out, errOut bytes.Buffer
	code := run(context.Background(), nil, env(map[string]string{"STORAGE": "memory", "LISTEN_ADDR": ln.Addr().String()}), &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "listen") {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
}
