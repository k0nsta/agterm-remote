package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The page shim serves a host's HTML page as a URL page, so agterm gives it no
// bridge: the page can reach only its own server, and through it only submit
// and close (plan: Technical Details > Host pages).
const (
	maxPageBody     = 64 << 10
	maxPageHandlers = 16
	maxPagesPerHost = 8
	pageIOTimeout   = 5 * time.Second
	// pageBindWait is how long a POST waits for the daemon to learn the page's
	// id: a page may submit on load, before the open call has answered.
	pageBindWait = 2 * time.Second
	// pagePollInterval is how often a waiting call looks for its page leaving
	// the overlay slots.
	pagePollInterval = 250 * time.Millisecond
)

// pageCSP keeps the page's JavaScript on its own server. It is a response
// header, which a page cannot remove; a <meta> tag could only add to it.
const pageCSP = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; form-action 'self'; " +
	"frame-src 'none'; base-uri 'none'"

// Page states: exactly one transition leaves pageOpen.
const (
	pageOpen      = "open"
	pageSubmitted = "submitted"
	pageDismissed = "dismissed"
)

// pageLive reports whether agterm still lists the page in the row's overlay
// slots.
type pageLive func(ctx context.Context, row, pageID string) (bool, error)

var errPageLimit = errors.New("too many open pages from this host")

// page is one host page: a token, the file behind it and what happened to it.
type page struct {
	token string
	host  string
	row   string
	// side is the pane the overlay is scoped to, "" on a row without a split.
	side string
	file string
	// dir is removed when the page is released.
	dir string

	mu     sync.Mutex
	state  string
	pageID string
	value  string
	bound  chan struct{}
	done   chan struct{}
}

func newPage(token, host, row, side, file, dir string) *page {
	return &page{
		token: token, host: host, row: row, side: side, file: file, dir: dir,
		state: pageOpen, bound: make(chan struct{}), done: make(chan struct{}),
	}
}

// bind records the page id agterm reported for the overlay. The first id wins.
func (p *page) bind(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pageID != "" {
		return
	}
	p.pageID = id
	close(p.bound)
}

func (p *page) id() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pageID
}

func (p *page) isOpen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == pageOpen
}

// transition moves an open page to state exactly once; it reports whether this
// call did it.
func (p *page) transition(state, value string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != pageOpen {
		return false
	}
	p.state, p.value = state, value
	close(p.done)
	return true
}

// outcome is the JSON agtermctl prints for a blocking page, and its exit code:
// 0 submitted, 2 dismissed.
func (p *page) outcome() (body []byte, exit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reply := struct {
		Outcome string `json:"outcome"`
		PageID  string `json:"pageID"`
		Value   string `json:"value,omitempty"`
	}{Outcome: p.state, PageID: p.pageID}
	exit = 2
	if p.state == pageSubmitted {
		reply.Value, exit = p.value, 0
	}
	body, _ = json.Marshal(reply)
	return append(body, '\n'), exit
}

// errRowMissing means the row is not in the tree; maxMissingPolls consecutive
// misses in wait dismiss the page so a closed row does not keep its token.
var errRowMissing = errors.New("not in the tree")

const maxMissingPolls = 8

// pageShim is the daemon's one loopback server for host pages.
type pageShim struct {
	live pageLive
	// bindWait is pageBindWait, a field so a test can shorten it.
	bindWait time.Duration
	handlers chan struct{}

	mu      sync.Mutex
	pages   map[string]*page
	perHost map[string]int
	// released remembers the most recent maxSpentTokens released tokens, so a
	// replayed POST gets 409 rather than 404.
	released  map[string]bool
	spentList []string
	ln        net.Listener
	srv       *http.Server
	addr      string
}

func newPageShim(live pageLive) *pageShim {
	return &pageShim{
		live: live, bindWait: pageBindWait,
		handlers: make(chan struct{}, maxPageHandlers),
		pages:    make(map[string]*page), perHost: make(map[string]int),
		released: make(map[string]bool),
	}
}

// serveLocked starts the loopback server on first use.
func (s *pageShim) serveLocked() error {
	if s.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start page server: %w", err)
	}
	s.ln, s.addr = ln, ln.Addr().String()
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: pageIOTimeout,
		ReadTimeout:       pageIOTimeout,
		WriteTimeout:      pageIOTimeout,
		IdleTimeout:       pageIOTimeout,
		MaxHeaderBytes:    16 << 10,
	}
	go func(srv *http.Server) { _ = srv.Serve(ln) }(s.srv)
	return nil
}

// close stops the server and drops every page.
func (s *pageShim) close() {
	s.mu.Lock()
	srv := s.srv
	pages := s.pages
	s.srv, s.ln, s.addr = nil, nil, ""
	s.pages, s.perHost = make(map[string]*page), make(map[string]int)
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	for _, p := range pages {
		removeDir(p.dir)
	}
}

// register adds a page for host and returns the URL to open. At most
// maxPagesPerHost pages of a host are open at once.
func (s *pageShim) register(host, row, side, file, dir string) (*page, string, error) {
	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perHost[host] >= maxPagesPerHost {
		return nil, "", errPageLimit
	}
	if err := s.serveLocked(); err != nil {
		return nil, "", err
	}
	p := newPage(token, host, row, side, file, dir)
	s.pages[token] = p
	s.perHost[host]++
	return p, "http://" + s.addr + "/" + token + "/", nil
}

// release drops the page's token and removes its temp directory.
func (s *pageShim) release(p *page) {
	s.mu.Lock()
	if _, ok := s.pages[p.token]; ok {
		delete(s.pages, p.token)
		s.remember(p.token)
		if s.perHost[p.host]--; s.perHost[p.host] <= 0 {
			delete(s.perHost, p.host)
		}
	}
	s.mu.Unlock()
	removeDir(p.dir)
}

// releaseHost drops every page of host (the host is going away).
func (s *pageShim) releaseHost(host string) {
	s.mu.Lock()
	var gone []*page
	for _, p := range s.pages {
		if p.host == host {
			gone = append(gone, p)
		}
	}
	s.mu.Unlock()
	for _, p := range gone {
		s.release(p)
	}
}

// openInSlot lists the open pages of host in a row's pane slot. More than one
// means a page was replaced and its watcher has not noticed yet. A page that
// covers the whole row (side "") is in every slot of it, so a call naming
// either pane reaches it.
func (s *pageShim) openInSlot(host, row, side string) []*page {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []*page
	for _, p := range s.pages {
		if p.host == host && strings.EqualFold(p.row, row) && (p.side == side || p.side == "") && p.isOpen() {
			found = append(found, p)
		}
	}
	return found
}

func (s *pageShim) lookup(token string) *page {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pages[token]
}

// maxSpentTokens bounds the memory of released tokens.
const maxSpentTokens = 1024

// remember records a released token; s.mu must be held.
func (s *pageShim) remember(token string) {
	if s.released == nil {
		s.released = make(map[string]bool)
	}
	s.released[token] = true
	s.spentList = append(s.spentList, token)
	if len(s.spentList) > maxSpentTokens {
		delete(s.released, s.spentList[0])
		s.spentList = s.spentList[1:]
	}
}

// spent reports whether token belonged to a page that was released.
func (s *pageShim) spent(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released[token]
}

func (s *pageShim) address() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// wait blocks until the page is submitted or dismissed, or ctx ends. It
// dismisses the page itself when agterm stops listing it (⌘W, the panel's
// button, a reload into another page).
func (s *pageShim) wait(ctx context.Context, p *page, interval time.Duration) bool {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missing := 0
	for {
		select {
		case <-p.done:
			return true
		case <-ctx.Done():
			return false
		case <-ticker.C:
			live, err := s.live(ctx, p.row, p.id())
			switch {
			case err == nil && !live:
				p.transition(pageDismissed, "")
			case errors.Is(err, errRowMissing):
				// The row closed (or the tree is limited to another window
				// for a while): give up only after it stays missing.
				if missing++; missing >= maxMissingPolls {
					p.transition(pageDismissed, "")
				}
			default:
				missing = 0
			}
		}
	}
}

func randomToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func removeDir(dir string) {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

func (s *pageShim) serveHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case s.handlers <- struct{}{}:
		defer func() { <-s.handlers }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	addr := s.address()
	if addr == "" || r.Host != addr {
		http.Error(w, "wrong host", http.StatusMisdirectedRequest)
		return
	}
	token, action, ok := splitPagePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := s.lookup(token)
	if p == nil {
		// A replayed POST on a released token changes nothing and is a conflict.
		if r.Method == http.MethodPost && (action == "submit" || action == "close") && s.spent(token) {
			http.Error(w, "already finished", http.StatusConflict)
			return
		}
		http.NotFound(w, r)
		return
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.servePage(w, p)
	case (action == "submit" || action == "close") && r.Method == http.MethodPost:
		s.servePost(w, r, p, action, addr)
	default:
		http.NotFound(w, r)
	}
}

// splitPagePath splits /<token>/ and /<token>/<action>; nothing else is served.
func splitPagePath(path string) (token, action string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] == "":
		return parts[0], "", true
	case len(parts) == 2 && parts[0] != "" && (parts[1] == "submit" || parts[1] == "close"):
		return parts[0], parts[1], true
	}
	return "", "", false
}

func (s *pageShim) servePage(w http.ResponseWriter, p *page) {
	if !p.isOpen() {
		http.Error(w, "gone", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(p.file)
	if err != nil {
		http.Error(w, "gone", http.StatusNotFound)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", pageCSP)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(injectShim(data, p.token))
}

func (s *pageShim) servePost(w http.ResponseWriter, r *http.Request, p *page, action, addr string) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+addr {
		http.Error(w, "wrong origin", http.StatusForbidden)
		return
	}
	// A body over the limit is refused without being read.
	// Connection: close also keeps the server from draining it first.
	if r.ContentLength > maxPageBody {
		w.Header().Set("Connection", "close")
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	value := ""
	if action == "submit" {
		var body struct {
			Value *string `json:"value"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPageBody))
		if err := decoder.Decode(&body); err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "bad body", status)
			return
		}
		if body.Value == nil {
			http.Error(w, "value must be a string", http.StatusBadRequest)
			return
		}
		value = *body.Value
	}
	if !s.current(r.Context(), p) {
		http.Error(w, "page is not open", http.StatusConflict)
		return
	}
	state := pageDismissed
	if action == "submit" {
		state = pageSubmitted
	}
	if !p.transition(state, value) {
		http.Error(w, "page is not open", http.StatusConflict)
		return
	}
	_, _ = io.WriteString(w, `{"ok":true}`)
}

// current reports whether p is open and still the page agterm lists under the
// id it reported, so a POST can never act for a page that was replaced.
func (s *pageShim) current(ctx context.Context, p *page) bool {
	if !p.isOpen() {
		return false
	}
	select {
	case <-p.bound:
	case <-time.After(s.bindWait):
		return false
	case <-ctx.Done():
		return false
	}
	live, err := s.live(ctx, p.row, p.id())
	return err == nil && live
}

var headTag = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
var htmlTag = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)

// injectShim puts the shim script first in <head>: after the head tag, else in
// a head made after <html>, else at the start.
func injectShim(data []byte, token string) []byte {
	script := []byte(shimScript(token))
	if loc := headTag.FindIndex(data); loc != nil {
		return splice(data, loc[1], script)
	}
	if loc := htmlTag.FindIndex(data); loc != nil {
		return splice(data, loc[1], append(append([]byte("<head>"), script...), "</head>"...))
	}
	return append(script, data...)
}

func splice(data []byte, at int, insert []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data) + len(insert))
	out.Write(data[:at])
	out.Write(insert)
	out.Write(data[at:])
	return out.Bytes()
}

// shimScript defines window.agterm.request for a host page: submit and close
// go to this page's own server, every other command is rejected. It also
// gives data-agterm buttons and forms agterm's page-bridge meaning (args from
// data-agterm-args and the form's named fields, the reply or error in
// data-agterm-into), routed through that same request, so a page written for
// a local overlay submits the same way.
func shimScript(token string) string {
	return `<script>(function(){var T=` + fmt.Sprintf("%q", token) + `;
function post(action,body){return fetch('/'+T+'/'+action,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(r){
if(!r.ok){throw new Error('page is not open ('+r.status+')');}return {ok:true};});}
window.agterm={request:function(cmd,opts){var args=(opts&&opts.args)||{};
if(cmd==='session.overlay.submit'){if(typeof args.value!=='string'){return Promise.reject(new Error('value must be a string'));}return post('submit',{value:args.value});}
if(cmd==='session.overlay.close'){return post('close',{});}
return Promise.reject(new Error('not available to pages from a host'));}};
window.close=function(){post('close',{}).catch(function(){});};
function run(el,fields){var args={};try{args=JSON.parse(el.getAttribute('data-agterm-args')||'{}')||{};}catch(e){}
for(var k in fields){args[k]=fields[k];}var into=el.getAttribute('data-agterm-into');
function show(t){var n=into&&document.querySelector(into);if(n){n.textContent=t;}}
window.agterm.request(el.getAttribute('data-agterm'),{args:args}).then(function(){show('ok');},function(e){show(String(e&&e.message||e));});}
document.addEventListener('click',function(e){var el=e.target&&e.target.closest&&e.target.closest('[data-agterm]');
if(!el||el.tagName==='FORM'){return;}e.preventDefault();run(el,{});},true);
document.addEventListener('submit',function(e){var f=e.target;if(!f||!f.hasAttribute||!f.hasAttribute('data-agterm')){return;}
e.preventDefault();var fields={};for(var i=0;i<f.elements.length;i++){var x=f.elements[i];if(!x.name){continue;}
fields[x.name]=x.type==='checkbox'?x.checked:(x.type==='number'?Number(x.value):x.value);}run(f,fields);},true);
})();</script>`
}
