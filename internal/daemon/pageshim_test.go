package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// liveSet is a pageLive that reports the pages in set as listed.
type liveSet struct {
	mu  sync.Mutex
	ids map[string]bool
	err error
}

func newLiveSet(t *testing.T, ids ...string) *liveSet {
	t.Helper()
	set := &liveSet{ids: make(map[string]bool)}
	for _, id := range ids {
		set.ids[id] = true
	}
	return set
}

func (l *liveSet) live(_ context.Context, _, pageID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ids[pageID], l.err
}

func (l *liveSet) drop(t *testing.T, id string) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ids, id)
}

func newTestShim(t *testing.T, live pageLive) *pageShim {
	t.Helper()
	shim := newPageShim(live)
	t.Cleanup(shim.close)
	return shim
}

// openTestPage registers a page for host "homelab" with a file in a temp dir,
// bound to page id "P1".
func openTestPage(t *testing.T, shim *pageShim, html string) (*page, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "0.html")
	if err := os.WriteFile(file, []byte(html), 0o600); err != nil {
		t.Fatalf("write page: %v", err)
	}
	p, url, err := shim.register("homelab", "row-1", "left", file, dir)
	if err != nil {
		t.Fatalf("register() error = %v", err)
	}
	p.bind("P1")
	return p, url
}

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	var body strings.Builder
	reader := bufio.NewReader(response.Body)
	if _, err := reader.WriteTo(&body); err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return response, body.String()
}

func TestPageShimServesThePageWithCSPAndShimFirst(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	_, url := openTestPage(t, shim, `<!doctype html><html><head><title>t</title></head><body>hello</body></html>`)
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/") {
		t.Fatalf("url = %q, want http://127.0.0.1:<port>/<token>/", url)
	}
	response, body := get(t, url)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Security-Policy"); got != pageCSP {
		t.Fatalf("CSP = %q, want %q", got, pageCSP)
	}
	for _, directive := range []string{"default-src 'none'", "connect-src 'self'", "form-action 'self'", "frame-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(pageCSP, directive) {
			t.Fatalf("CSP lacks %q", directive)
		}
	}
	if !strings.Contains(body, "<head><script>") || strings.Index(body, "<script>") > strings.Index(body, "<title>") {
		t.Fatalf("body = %q, want the shim first in <head>", body)
	}
	if !strings.Contains(body, "hello") || !strings.Contains(body, "window.close") || !strings.Contains(body, "not available to pages from a host") {
		t.Fatalf("body = %q, want the page and the shim", body)
	}
}

func TestInjectShimVariants(t *testing.T) {
	t.Helper()
	for _, tc := range []struct{ name, in, want, before string }{
		{"head with attributes", `<HEAD lang="en"><meta></HEAD>`, `<HEAD lang="en"><script>`, "<meta>"},
		{"no head", `<html><body>x</body></html>`, `<html><head><script>`, "<body>"},
		{"fragment", `<p>x</p>`, `<script>`, "<p>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(injectShim([]byte(tc.in), "tok"))
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("injectShim() = %q, want prefix %q", got, tc.want)
			}
			if strings.Index(got, "<script>") > strings.Index(got, tc.before) {
				t.Fatalf("injectShim() = %q, want the script before %s", got, tc.before)
			}
		})
	}
}

func TestPageShimSubmitAndOutcome(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	if response := post(t, url+"submit", `{"value":"main"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("submit status = %d, want 200", response.StatusCode)
	}
	if !shim.wait(context.Background(), p, time.Hour) {
		t.Fatal("wait() = false after a submit")
	}
	body, exit := p.outcome()
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("outcome %q: %v", body, err)
	}
	if exit != 0 || got["outcome"] != "submitted" || got["pageID"] != "P1" || got["value"] != "main" {
		t.Fatalf("outcome = %q exit %d, want submitted/P1/main exit 0", body, exit)
	}
}

func TestPageShimCloseIsDismissed(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	if response := post(t, url+"close", ``); response.StatusCode != http.StatusOK {
		t.Fatalf("close status = %d, want 200", response.StatusCode)
	}
	body, exit := p.outcome()
	if exit != 2 || !strings.Contains(string(body), `"outcome":"dismissed"`) || strings.Contains(string(body), "value") {
		t.Fatalf("outcome = %q exit %d, want dismissed exit 2 and no value", body, exit)
	}
}

func TestPageShimOneTransition(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	post(t, url+"submit", `{"value":"first"}`)
	for _, tc := range []struct{ action, body string }{{"submit", `{"value":"second"}`}, {"close", ``}} {
		if response := post(t, url+tc.action, tc.body); response.StatusCode != http.StatusConflict {
			t.Fatalf("%s after submit: status = %d, want 409", tc.action, response.StatusCode)
		}
	}
	if body, exit := p.outcome(); exit != 0 || !strings.Contains(string(body), `"value":"first"`) {
		t.Fatalf("outcome = %q exit %d, want the first submit unchanged", body, exit)
	}
}

func TestPageShimSubmitAfterCloseIsRefused(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	post(t, url+"close", ``)
	if response := post(t, url+"submit", `{"value":"late"}`); response.StatusCode != http.StatusConflict {
		t.Fatalf("submit after close: status = %d, want 409", response.StatusCode)
	}
	if body, exit := p.outcome(); exit != 2 || strings.Contains(string(body), "late") {
		t.Fatalf("outcome = %q exit %d, want dismissed", body, exit)
	}
}

func TestPageShimReplacedPageIsRefused(t *testing.T) {
	t.Helper()
	live := newLiveSet(t, "P1")
	shim := newTestShim(t, live.live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	live.drop(t, "P1") // another page took the slot
	if response := post(t, url+"submit", `{"value":"x"}`); response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a replaced page", response.StatusCode)
	}
	if !p.isOpen() {
		t.Fatal("a refused POST changed the page")
	}
}

func TestPageShimReplayedTokenAfterReleaseIsConflict(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	dir := p.dir
	post(t, url+"submit", `{"value":"x"}`)
	shim.release(p)
	if response := post(t, url+"submit", `{"value":"again"}`); response.StatusCode != http.StatusConflict {
		t.Fatalf("replayed token: status = %d, want 409", response.StatusCode)
	}
	if response, _ := get(t, url); response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after release: status = %d, want 404", response.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp directory after release: %v, want it removed", err)
	}
}

func TestPageShimGetOfAFinishedPageIsGone(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	_, url := openTestPage(t, shim, "<p>x</p>")
	post(t, url+"close", ``)
	if response, _ := get(t, url); response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET of a closed page: status = %d, want 404", response.StatusCode)
	}
}

func TestPageShimRefusesAnOversizeBodyUnread(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	address := strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/"+p.token+"/")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// Only the headers of a 65 KiB body are sent; the daemon must answer
	// without waiting for the body.
	request := "POST /" + p.token + "/submit HTTP/1.1\r\nHost: " + address + "\r\nContent-Type: application/json\r\nContent-Length: 66560\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(status, "413") {
		t.Fatalf("status line = %q, %v, want 413", status, err)
	}
	if !p.isOpen() {
		t.Fatal("a refused body changed the page")
	}
}

func TestPageShimRefusesAnOversizeChunkedBody(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	huge := `{"value":"` + strings.Repeat("a", 65<<10) + `"}`
	request, err := http.NewRequest(http.MethodPost, url+"submit", struct{ *strings.Reader }{strings.NewReader(huge)})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusRequestEntityTooLarge || !p.isOpen() {
		t.Fatalf("status = %d, open = %v, want 413 and no change", response.StatusCode, p.isOpen())
	}
}

func TestPageShimRejectsMalformedAndForeignRequests(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	base := strings.TrimSuffix(url, p.token+"/")
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		host   string
		origin string
		want   int
	}{
		{name: "value missing", method: "POST", path: url + "submit", body: `{}`, want: http.StatusBadRequest},
		{name: "value not a string", method: "POST", path: url + "submit", body: `{"value":3}`, want: http.StatusBadRequest},
		{name: "not json", method: "POST", path: url + "submit", body: `nope`, want: http.StatusBadRequest},
		{name: "wrong host", method: "POST", path: url + "close", host: "evil.test", want: http.StatusMisdirectedRequest},
		{name: "wrong origin", method: "POST", path: url + "close", origin: "http://evil.test", want: http.StatusForbidden},
		{name: "unknown token", method: "GET", path: base + "deadbeef/", want: http.StatusNotFound},
		{name: "root is no directory listing", method: "GET", path: base, want: http.StatusNotFound},
		{name: "other file", method: "GET", path: url + "other.html", want: http.StatusNotFound},
		{name: "GET on an action", method: "GET", path: url + "submit", want: http.StatusNotFound},
		{name: "POST on the page", method: "POST", path: url, want: http.StatusNotFound},
		{name: "unknown action", method: "POST", path: url + "type", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if tc.host != "" {
				request.Host = tc.host
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			_ = response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.want)
			}
			if !p.isOpen() {
				t.Fatal("a refused request changed the page")
			}
		})
	}
}

func TestPageShimRefusesTheSeventeenthHandler(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	shim.bindWait = 3 * time.Second
	dir := t.TempDir()
	file := filepath.Join(dir, "0.html")
	if err := os.WriteFile(file, []byte("<p>x</p>"), 0o600); err != nil {
		t.Fatalf("write page: %v", err)
	}
	// An unbound page keeps each POST waiting for its id: 16 handlers busy.
	_, url, err := shim.register("homelab", "row-1", "left", file, dir)
	if err != nil {
		t.Fatalf("register() error = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < maxPageHandlers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := http.Post(url+"close", "application/json", strings.NewReader(""))
			if err == nil {
				_ = response.Body.Close()
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(shim.handlers) < maxPageHandlers && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(shim.handlers); got != maxPageHandlers {
		t.Fatalf("busy handlers = %d, want %d", got, maxPageHandlers)
	}
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("17th handler: status = %d, want 503", response.StatusCode)
	}
	p := shim.lookup(strings.Split(strings.TrimPrefix(url, "http://"+shim.address()+"/"), "/")[0])
	p.bind("P1") // unblock the waiting handlers
	wg.Wait()
}

func TestPageShimRefusesTheNinthPage(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t).live)
	var first *page
	for i := 0; i < maxPagesPerHost; i++ {
		p, _, err := shim.register("homelab", "row", "", "f", "")
		if err != nil {
			t.Fatalf("register #%d error = %v", i+1, err)
		}
		if first == nil {
			first = p
		}
	}
	if _, _, err := shim.register("homelab", "row", "", "f", ""); err != errPageLimit {
		t.Fatalf("9th register error = %v, want %v", err, errPageLimit)
	}
	if _, _, err := shim.register("other", "row", "", "f", ""); err != nil {
		t.Fatalf("another host's register error = %v, want none", err)
	}
	shim.release(first)
	if _, _, err := shim.register("homelab", "row", "", "f", ""); err != nil {
		t.Fatalf("register after a release error = %v, want none", err)
	}
	shim.releaseHost("homelab")
	if _, _, err := shim.register("homelab", "row", "", "f", ""); err != nil {
		t.Fatalf("register after releaseHost error = %v, want none", err)
	}
}

func TestPageShimWaitsForTheIDOfALatePage(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P9").live)
	dir := t.TempDir()
	p, url, err := shim.register("homelab", "row-1", "", "f", dir)
	if err != nil {
		t.Fatalf("register() error = %v", err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		p.bind("P9") // the open call answers after the page already submitted
	}()
	if response := post(t, url+"submit", `{"value":"early"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("early submit: status = %d, want 200", response.StatusCode)
	}
}

func TestPageShimGivesUpOnAPageThatNeverBinds(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t).live)
	shim.bindWait = 50 * time.Millisecond
	p, url, err := shim.register("homelab", "row-1", "", "f", "")
	if err != nil {
		t.Fatalf("register() error = %v", err)
	}
	if response := post(t, url+"close", ``); response.StatusCode != http.StatusConflict || !p.isOpen() {
		t.Fatalf("status = %d, open = %v, want 409 and no change", response.StatusCode, p.isOpen())
	}
}

func TestPageShimConcurrentSubmitsOneWins(t *testing.T) {
	t.Helper()
	shim := newTestShim(t, newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	var ok, conflict atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := http.Post(url+"submit", "application/json", strings.NewReader(`{"value":"v"}`))
			if err != nil {
				return
			}
			defer func() { _ = response.Body.Close() }()
			switch response.StatusCode {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusConflict:
				conflict.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || conflict.Load() != 7 {
		t.Fatalf("ok = %d, conflict = %d, want exactly one winner", ok.Load(), conflict.Load())
	}
	if !p.isOpen() {
		return
	}
	t.Fatal("page still open after a winning submit")
}

func TestPageShimWaitDismissesAPageThatLeavesTheSlot(t *testing.T) {
	t.Helper()
	live := newLiveSet(t, "P1")
	shim := newTestShim(t, live.live)
	p, _ := openTestPage(t, shim, "<p>x</p>")
	go func() {
		time.Sleep(50 * time.Millisecond)
		live.drop(t, "P1") // ⌘W, the panel button, or a reload into another page
	}()
	if !shim.wait(context.Background(), p, 10*time.Millisecond) {
		t.Fatal("wait() = false, want the page dismissed")
	}
	if body, exit := p.outcome(); exit != 2 || !strings.Contains(string(body), "dismissed") {
		t.Fatalf("outcome = %q exit %d, want dismissed exit 2", body, exit)
	}
}

func TestPageShimWaitIgnoresReadErrorsAndStopsOnContext(t *testing.T) {
	t.Helper()
	live := newLiveSet(t)
	live.err = context.DeadlineExceeded
	shim := newTestShim(t, live.live)
	p, _ := openTestPage(t, shim, "<p>x</p>")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if shim.wait(ctx, p, 10*time.Millisecond) {
		t.Fatal("wait() = true, want it to keep waiting through tree read errors")
	}
	if !p.isOpen() {
		t.Fatal("a failed read dismissed the page")
	}
}

func TestPageShimCloseDropsEverything(t *testing.T) {
	t.Helper()
	shim := newPageShim(newLiveSet(t, "P1").live)
	p, url := openTestPage(t, shim, "<p>x</p>")
	dir := p.dir
	shim.close()
	if _, err := http.Get(url); err == nil {
		t.Fatal("GET after close succeeded, want the server down")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temp directory after close: %v, want it removed", err)
	}
}
