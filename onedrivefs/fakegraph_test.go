package onedrivefs

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeToken = "test-token"

	// fakeFragmentUnit is the size that every fragment of an upload
	// session except the last must be a multiple of, like in Graph
	fakeFragmentUnit = 320 << 10
)

// fakeGraph is an in-memory fake of the drive API of Microsoft Graph,
// served by an httptest.Server, with the semantics of the real API that
// the file system relies on, so that the conformance suite runs offline:
//
//   - the Graph API requires the access token, the pre-authenticated URLs
//     of downloads, upload sessions and copy monitors reject it;
//   - items are addressed by path (root:/path:/action), case-insensitively,
//     and keep the case of their creation; a parent that is a file
//     doesn't exist for the path;
//   - uploads create missing parent folders, an upload onto a folder
//     is a conflict, a file in place of a parent a bad request;
//   - upload sessions need the size in every Content-Range, every
//     fragment but the last must be a multiple of 320 KiB;
//   - creating a folder and moving onto an existing name are conflicts,
//     moving a folder into itself is a bad request;
//   - copies are asynchronous: POST copy returns a monitor URL, which
//     reports a name conflict as failed copy;
//   - deletes are recursive, If-Match only deletes that eTag;
//   - listings are paginated with absolute @odata.nextLink URLs
//     and $skiptoken cursors;
//   - a GET of the content of a folder is not found.
type fakeGraph struct {
	srv     *httptest.Server
	driveID string

	mtx      sync.Mutex
	items    map[string]*fakeItem // by lower case path, "/" is the root
	sessions map[string]*fakeSession
	monitors map[string]*fakeMonitor
	cursors  map[string][]*fakeItem // remaining entries of a listing by $skiptoken
	pageSize int                    // entries per listing page
	counter  int                    // for ids, eTags, sessions and monitors
	calls    map[string]int         // number of calls per operation
	leaks    int                    // requests to pre-authenticated URLs with a token
	failNext map[string][]fakeFailure
	hooks    map[string]func() // runs once at the next call of an operation
}

type fakeItem struct {
	path       string // in the case of its creation
	isDir      bool
	data       []byte
	id         string
	version    int
	modified   time.Time
	fsModified time.Time // fileSystemInfo, set by PATCH
}

func (it *fakeItem) eTag() string {
	return fmt.Sprintf(`"{%s},%d"`, it.id, it.version)
}

type fakeSession struct {
	path  string
	data  []byte
	total int64 // -1 before the first fragment
}

type fakeMonitor struct {
	polls  int // remaining polls that report inProgress
	failed string
	id     string // of the new item
}

// fakeFailure is the response of a failing call, a zero
// status lets the call proceed.
type fakeFailure struct {
	status     int
	code       string
	retryAfter string
}

func newFakeGraph(t *testing.T, driveID string) *fakeGraph {
	g := &fakeGraph{
		driveID:  driveID,
		items:    map[string]*fakeItem{"/": {path: "/", isDir: true, id: "root-id", version: 1}},
		sessions: map[string]*fakeSession{},
		monitors: map[string]*fakeMonitor{},
		cursors:  map[string][]*fakeItem{},
		pageSize: 3,
		calls:    map[string]int{},
		failNext: map[string][]fakeFailure{},
		hooks:    map[string]func(){},
	}
	g.srv = httptest.NewServer(g)
	t.Cleanup(g.srv.Close)
	return g
}

// graphURL is the Options.GraphURL of the fake.
func (g *fakeGraph) graphURL() string {
	return g.srv.URL + "/v1.0"
}

// callCount returns the number of calls of an operation.
func (g *fakeGraph) callCount(op string) int {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	return g.calls[op]
}

// setFailNext makes the next calls of an operation fail with failures.
func (g *fakeGraph) setFailNext(op string, failures ...fakeFailure) {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	g.failNext[op] = failures
}

// onCall makes hook run once at the next call of an operation, before
// the call does anything else, like another client changing the drive
// between two requests of the file system. hook runs with the fake
// locked, so it must change the items directly.
func (g *fakeGraph) onCall(op string, hook func()) {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	g.hooks[op] = hook
}

// get returns the item at the path p, case-insensitively.
func (g *fakeGraph) get(p string) *fakeItem {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	return g.items[strings.ToLower(p)]
}

// put stores a file at the path p for a test, creating its parents.
func (g *fakeGraph) put(p string, data []byte) *fakeItem {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	it, _, _ := g.upload(p, data, true)
	return it
}

func (g *fakeGraph) nextID() string {
	g.counter++
	return strconv.Itoa(g.counter)
}

// begin counts the call of op, runs its hook and returns the next
// failure set for op. The fake must be locked.
func (g *fakeGraph) begin(op string) fakeFailure {
	g.calls[op]++
	if hook := g.hooks[op]; hook != nil {
		delete(g.hooks, op)
		hook()
	}
	if failures := g.failNext[op]; len(failures) > 0 {
		g.failNext[op] = failures[1:]
		return failures[0]
	}
	return fakeFailure{}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func (g *fakeGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	if id, ok := strings.CutPrefix(escaped, "/download/"); ok {
		// Written without the lock, a reader that stops
		// reading must not block the other requests
		data, status := g.download(r, id)
		if status != http.StatusOK {
			writeError(w, status, "itemNotFound", "can't download")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
		return
	}

	g.mtx.Lock()
	defer g.mtx.Unlock()
	if kind, id, ok := strings.Cut(strings.TrimPrefix(escaped, "/"), "/"); ok && (kind == "upload" || kind == "monitor") {
		if r.Header.Get("Authorization") != "" {
			g.leaks++
			writeError(w, http.StatusUnauthorized, "unauthenticated", "pre-authenticated URLs reject an Authorization header")
			return
		}
		switch kind {
		case "upload":
			g.serveUpload(w, r, id)
		case "monitor":
			g.serveMonitor(w, id)
		}
		return
	}

	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeError(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token is empty or invalid.")
		return
	}
	driveURL := "/v1.0/drives/" + url.PathEscape(g.driveID)
	if rest, ok := strings.CutPrefix(escaped, "/v1.0/drives/"+g.driveID); ok {
		escaped = driveURL + rest // The drive id without escaped "!"
	}
	switch {
	case escaped == "/v1.0/me/drive" || escaped == driveURL:
		if fail := g.begin("GetDrive"); fail.status != 0 {
			writeFailure(w, fail)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": g.driveID, "driveType": "business"})
		return
	case !strings.HasPrefix(escaped, driveURL+"/root"):
		writeError(w, http.StatusNotFound, "itemNotFound", "unknown resource")
		return
	}
	itemPath, action := "/", strings.TrimPrefix(escaped, driveURL+"/root")
	if rest, ok := strings.CutPrefix(action, ":"); ok {
		escapedPath, act, _ := strings.Cut(rest, ":")
		unescaped, err := url.PathUnescape(escapedPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalidRequest", err.Error())
			return
		}
		itemPath, action = path.Clean("/"+unescaped), act
	}

	op := r.Method + " " + action
	switch op {
	case "GET ":
		op = "GetItem"
	case "PATCH ":
		op = "Patch"
	case "DELETE ":
		op = "Delete"
	case "GET /children":
		op = "ListChildren"
	case "POST /children":
		op = "CreateFolder"
	case "GET /content":
		op = "GetContent"
	case "PUT /content":
		op = "PutContent"
	case "POST /createUploadSession":
		op = "CreateUploadSession"
	case "POST /copy":
		op = "Copy"
	default:
		writeError(w, http.StatusBadRequest, "invalidRequest", "unsupported request "+op)
		return
	}
	if fail := g.begin(op); fail.status != 0 {
		writeFailure(w, fail)
		return
	}
	var body struct {
		Name            string `json:"name"`
		Conflict        string `json:"@microsoft.graph.conflictBehavior"`
		ParentReference *struct {
			ID string `json:"id"`
		} `json:"parentReference"`
		FileSystemInfo *struct {
			LastModified time.Time `json:"lastModifiedDateTime"`
		} `json:"fileSystemInfo"`
		Item *struct {
			Conflict string `json:"@microsoft.graph.conflictBehavior"`
		} `json:"item"`
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidRequest", err.Error())
		return
	}
	if r.Header.Get("Content-Type") == "application/json" {
		if err := json.Unmarshal(data, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalidRequest", err.Error())
			return
		}
	}

	if op == "PutContent" || op == "CreateUploadSession" {
		replace := r.URL.Query().Get("@microsoft.graph.conflictBehavior") != "fail"
		if op == "CreateUploadSession" {
			if status, code := g.checkUpload(itemPath, true); status != 0 {
				writeError(w, status, code, "can't upload to "+itemPath)
				return
			}
			id := g.nextID()
			g.sessions[id] = &fakeSession{path: itemPath, total: -1}
			writeJSON(w, http.StatusOK, map[string]any{"uploadUrl": g.srv.URL + "/upload/" + id + "?tempauth=secret-" + id})
			return
		}
		it, status, code := g.upload(itemPath, data, replace)
		if it == nil {
			writeError(w, status, code, "can't upload to "+itemPath)
			return
		}
		writeJSON(w, status, g.itemJSON(it))
		return
	}

	it := g.lookup(itemPath)
	if it == nil {
		writeError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	}
	switch op {
	case "GetItem":
		writeJSON(w, http.StatusOK, g.itemJSON(it))
	case "ListChildren":
		g.serveChildren(w, r, it)
	case "GetContent":
		if it.isDir {
			writeError(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
			return
		}
		w.Header().Set("Location", g.srv.URL+"/download/"+it.id+"?tempauth=secret")
		w.WriteHeader(http.StatusFound)
	case "CreateFolder":
		if !it.isDir {
			writeError(w, http.StatusBadRequest, "invalidRequest", "the parent is not a folder")
			return
		}
		g.serveCreateFolder(w, it, body.Name)
	case "Delete":
		if match := r.Header.Get("If-Match"); match != "" && match != it.eTag() {
			writeError(w, http.StatusPreconditionFailed, "resourceModified", "ETag does not match current item's value")
			return
		}
		if it.path == "/" {
			writeError(w, http.StatusForbidden, "accessDenied", "can't delete the root")
			return
		}
		g.deleteTree(it.path)
		w.WriteHeader(http.StatusNoContent)
	case "Patch":
		if body.FileSystemInfo != nil {
			it.fsModified = body.FileSystemInfo.LastModified
			it.version++
		}
		if body.Name == "" && body.ParentReference == nil {
			writeJSON(w, http.StatusOK, g.itemJSON(it))
			return
		}
		parent := g.items[strings.ToLower(path.Dir(it.path))]
		if body.ParentReference != nil {
			parent = g.byID(body.ParentReference.ID)
		}
		name := cmp.Or(body.Name, path.Base(it.path))
		g.serveMove(w, it, parent, name)
	case "Copy":
		g.serveCopy(w, it, body.ParentReference, body.Name)
	}
}

func writeFailure(w http.ResponseWriter, fail fakeFailure) {
	if fail.retryAfter != "" {
		w.Header().Set("Retry-After", fail.retryAfter)
	}
	writeError(w, fail.status, cmp.Or(fail.code, "generalException"), "injected failure")
}

// lookup returns the item at p, nil if it or a parent folder is missing
// or a parent is a file. The fake must be locked.
func (g *fakeGraph) lookup(p string) *fakeItem {
	for dir := path.Dir(p); dir != "/"; dir = path.Dir(dir) {
		if parent := g.items[strings.ToLower(dir)]; parent == nil || !parent.isDir {
			return nil
		}
	}
	return g.items[strings.ToLower(p)]
}

func (g *fakeGraph) byID(id string) *fakeItem {
	for _, it := range g.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

// validName reports whether OneDrive accepts name.
func validName(name string) bool {
	return name != "" && !strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || strings.ContainsRune(`"*:<>?/\|`, r) })
}

// checkUpload returns the error status and code of an upload to p:
// a folder at p is a conflict, a file in place of a parent or an
// invalid name a bad request. With replace an existing file is not
// a conflict. The fake must be locked.
func (g *fakeGraph) checkUpload(p string, replace bool) (int, string) {
	if p == "/" || !validName(path.Base(p)) {
		return http.StatusBadRequest, "invalidRequest"
	}
	for dir := path.Dir(p); dir != "/"; dir = path.Dir(dir) {
		if parent := g.items[strings.ToLower(dir)]; parent != nil && !parent.isDir {
			return http.StatusBadRequest, "invalidRequest"
		}
	}
	if it := g.items[strings.ToLower(p)]; it != nil && (it.isDir || !replace) {
		return http.StatusConflict, "nameAlreadyExists"
	}
	return 0, ""
}

// upload creates or replaces the file at p with data, creating missing
// parent folders, and returns it with the status 201 for a new and 200
// for a replaced file, or nil with the error status and code.
// The fake must be locked.
func (g *fakeGraph) upload(p string, data []byte, replace bool) (*fakeItem, int, string) {
	if status, code := g.checkUpload(p, replace); status != 0 {
		return nil, status, code
	}
	parent := g.items["/"]
	for _, name := range strings.Split(p[1:], "/")[:strings.Count(p, "/")-1] {
		key := strings.ToLower(path.Join(parent.path, name))
		next := g.items[key]
		if next == nil {
			next = &fakeItem{path: path.Join(parent.path, name), isDir: true, id: g.nextID(), version: 1, modified: time.Now()}
			g.items[key] = next
		}
		parent = next
	}
	key := strings.ToLower(p)
	if it := g.items[key]; it != nil {
		it.data = slices.Clone(data)
		it.version++
		it.modified = time.Now()
		return it, http.StatusOK, ""
	}
	it := &fakeItem{path: path.Join(parent.path, path.Base(p)), data: slices.Clone(data), id: g.nextID(), version: 1, modified: time.Now()}
	g.items[key] = it
	return it, http.StatusCreated, ""
}

// children returns the direct children of the folder dir sorted by name.
// The fake must be locked.
func (g *fakeGraph) children(dir *fakeItem) []*fakeItem {
	var children []*fakeItem
	for key, it := range g.items {
		if key != "/" && strings.EqualFold(path.Dir(it.path), dir.path) {
			children = append(children, it)
		}
	}
	slices.SortFunc(children, func(a, b *fakeItem) int { return strings.Compare(a.path, b.path) })
	return children
}

func (g *fakeGraph) itemJSON(it *fakeItem) map[string]any {
	m := map[string]any{
		"id":                   it.id,
		"name":                 path.Base(it.path),
		"eTag":                 it.eTag(),
		"size":                 len(it.data),
		"lastModifiedDateTime": it.modified,
	}
	if !it.fsModified.IsZero() {
		m["fileSystemInfo"] = map[string]any{"lastModifiedDateTime": it.fsModified}
	}
	switch {
	case it.path == "/":
		m["name"] = "root"
		m["root"] = map[string]any{}
		m["folder"] = map[string]any{"childCount": len(g.children(it))}
	case it.isDir:
		m["folder"] = map[string]any{"childCount": len(g.children(it))}
	default:
		m["file"] = map[string]any{"mimeType": "application/octet-stream"}
	}
	return m
}

func (g *fakeGraph) serveChildren(w http.ResponseWriter, r *http.Request, dir *fakeItem) {
	if !dir.isDir {
		writeError(w, http.StatusBadRequest, "invalidRequest", "the item is not a folder")
		return
	}
	// The pages are a snapshot of the first request,
	// changes during the listing don't move its entries
	remaining := g.children(dir)
	if token := r.URL.Query().Get("$skiptoken"); token != "" {
		cursor, ok := g.cursors[token]
		if !ok {
			writeError(w, http.StatusBadRequest, "invalidRequest", "unknown $skiptoken")
			return
		}
		delete(g.cursors, token)
		remaining = cursor
	}
	page := remaining[:min(g.pageSize, len(remaining))]
	values := make([]map[string]any, len(page))
	for i, it := range page {
		values[i] = g.itemJSON(it)
	}
	result := map[string]any{"value": values}
	if len(remaining) > len(page) {
		token := g.nextID()
		g.cursors[token] = remaining[len(page):]
		query := r.URL.Query()
		query.Set("$skiptoken", token)
		result["@odata.nextLink"] = g.srv.URL + r.URL.EscapedPath() + "?" + query.Encode()
	}
	writeJSON(w, http.StatusOK, result)
}

func (g *fakeGraph) serveCreateFolder(w http.ResponseWriter, parent *fakeItem, name string) {
	p := path.Join(parent.path, name)
	if !validName(name) {
		writeError(w, http.StatusBadRequest, "invalidRequest", "invalid name")
		return
	}
	if g.items[strings.ToLower(p)] != nil {
		writeError(w, http.StatusConflict, "nameAlreadyExists", "Name already exists")
		return
	}
	it := &fakeItem{path: p, isDir: true, id: g.nextID(), version: 1, modified: time.Now()}
	g.items[strings.ToLower(p)] = it
	writeJSON(w, http.StatusCreated, g.itemJSON(it))
}

// deleteTree deletes the item at p and everything below it.
// The fake must be locked.
func (g *fakeGraph) deleteTree(p string) {
	key := strings.ToLower(p)
	maps.DeleteFunc(g.items, func(k string, _ *fakeItem) bool {
		return k == key || strings.HasPrefix(k, key+"/")
	})
}

func (g *fakeGraph) serveMove(w http.ResponseWriter, it, parent *fakeItem, name string) {
	switch {
	case parent == nil || !parent.isDir:
		writeError(w, http.StatusBadRequest, "invalidRequest", "the parent is not a folder")
		return
	case !validName(name):
		writeError(w, http.StatusBadRequest, "invalidRequest", "invalid name")
		return
	case it.path == "/" || isInside(it.path, parent.path) || strings.EqualFold(it.path, parent.path):
		writeError(w, http.StatusBadRequest, "invalidRequest", "can't move a folder into itself")
		return
	}
	oldKey := strings.ToLower(it.path)
	newPath := path.Join(parent.path, name)
	newKey := strings.ToLower(newPath)
	if other := g.items[newKey]; other != nil && other != it {
		writeError(w, http.StatusConflict, "nameAlreadyExists", "Name already exists")
		return
	}
	oldPath := it.path
	moved := map[string]*fakeItem{}
	maps.DeleteFunc(g.items, func(k string, item *fakeItem) bool {
		if k == oldKey || strings.HasPrefix(k, oldKey+"/") {
			item.path = newPath + item.path[len(oldPath):]
			moved[strings.ToLower(item.path)] = item
			return true
		}
		return false
	})
	maps.Copy(g.items, moved)
	it.version++
	writeJSON(w, http.StatusOK, g.itemJSON(it))
}

func (g *fakeGraph) serveCopy(w http.ResponseWriter, it *fakeItem, parentRef *struct {
	ID string `json:"id"`
}, name string) {
	if parentRef == nil {
		writeError(w, http.StatusBadRequest, "invalidRequest", "no parentReference")
		return
	}
	parent := g.byID(parentRef.ID)
	if parent == nil || !parent.isDir {
		writeError(w, http.StatusBadRequest, "invalidRequest", "the parent is not a folder")
		return
	}
	name = cmp.Or(name, path.Base(it.path))
	monitor := &fakeMonitor{polls: 1}
	newPath := path.Join(parent.path, name)
	if g.items[strings.ToLower(newPath)] != nil {
		monitor.failed = "nameAlreadyExists"
	} else {
		for _, src := range slices.Collect(maps.Values(g.items)) {
			if src == it || strings.HasPrefix(strings.ToLower(src.path), strings.ToLower(it.path)+"/") {
				p := newPath + src.path[len(it.path):]
				copied := &fakeItem{path: p, isDir: src.isDir, data: slices.Clone(src.data), id: g.nextID(), version: 1, modified: time.Now()}
				g.items[strings.ToLower(p)] = copied
				if src == it {
					monitor.id = copied.id
				}
			}
		}
	}
	id := g.nextID()
	g.monitors[id] = monitor
	w.Header().Set("Location", g.srv.URL+"/monitor/"+id)
	w.WriteHeader(http.StatusAccepted)
}

func (g *fakeGraph) serveMonitor(w http.ResponseWriter, id string) {
	if fail := g.begin("Monitor"); fail.status != 0 {
		writeFailure(w, fail)
		return
	}
	monitor := g.monitors[id]
	switch {
	case monitor == nil:
		writeError(w, http.StatusNotFound, "itemNotFound", "unknown monitor")
	case monitor.polls > 0:
		monitor.polls--
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "inProgress", "percentageComplete": 50})
	case monitor.failed != "":
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "error": map[string]any{"code": monitor.failed, "message": "copy failed"}})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "resourceId": monitor.id})
	}
}

// download returns the content of the file with the id
// and the status of the download.
func (g *fakeGraph) download(r *http.Request, id string) ([]byte, int) {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	if r.Header.Get("Authorization") != "" {
		g.leaks++
		return nil, http.StatusUnauthorized
	}
	if fail := g.begin("Download"); fail.status != 0 {
		return nil, fail.status
	}
	it := g.byID(id)
	if it == nil || it.isDir {
		return nil, http.StatusNotFound
	}
	return slices.Clone(it.data), http.StatusOK
}

func (g *fakeGraph) serveUpload(w http.ResponseWriter, r *http.Request, id string) {
	session := g.sessions[id]
	if session == nil {
		writeError(w, http.StatusNotFound, "itemNotFound", "The upload session was not found.")
		return
	}
	if r.Method == http.MethodDelete {
		g.calls["CancelUploadSession"]++
		delete(g.sessions, id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if fail := g.begin("UploadFragment"); fail.status != 0 {
		writeFailure(w, fail)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidRequest", err.Error())
		return
	}
	var start, end, total int64
	if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil {
		writeError(w, http.StatusBadRequest, "invalidRange", "Content-Range "+r.Header.Get("Content-Range"))
		return
	}
	switch {
	case session.total >= 0 && total != session.total:
		writeError(w, http.StatusBadRequest, "invalidRange", "the size of the file changed")
		return
	case start != int64(len(session.data)):
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "invalidRange", "unexpected fragment start")
		return
	case end-start+1 != int64(len(data)):
		writeError(w, http.StatusBadRequest, "invalidRange", "the fragment doesn't match its range")
		return
	case end+1 < total && len(data)%fakeFragmentUnit != 0:
		writeError(w, http.StatusBadRequest, "invalidRange", "fragments must be multiples of 320 KiB")
		return
	}
	session.total = total
	session.data = append(session.data, data...)
	if end+1 < total {
		writeJSON(w, http.StatusAccepted, map[string]any{"nextExpectedRanges": []string{fmt.Sprintf("%d-", end+1)}})
		return
	}
	delete(g.sessions, id)
	it, status, code := g.upload(session.path, session.data, true)
	if it == nil {
		writeError(w, status, code, "can't commit the upload to "+session.path)
		return
	}
	writeJSON(w, status, g.itemJSON(it))
}
