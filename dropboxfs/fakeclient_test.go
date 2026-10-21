package dropboxfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
)

// fakeClient is an in-memory fake of the Dropbox files API that implements
// the methods of files.ContextClient used by the file system with the
// semantics of the real API, so that the conformance suite runs offline:
//
//   - paths are case-insensitive, entries keep the case of their creation;
//   - uploads, folder creation, copies and moves create missing parent
//     folders, a file in the way of a parent is a file_ancestor conflict;
//   - relocations and folder creation onto an existing path are conflicts,
//     relocations that only change the case of a path as well;
//   - deletes are recursive, ParentRev only deletes that version of a file;
//   - listings are paginated, recursive ones include the folder itself;
//   - an upload request may carry at most maxUpload bytes.
//
// The errors have the types and tags of the SDK. Calling any other method
// panics, because the embedded interface is nil.
type fakeClient struct {
	files.ContextClient

	mtx       sync.Mutex
	entries   map[string]*fakeEntry // by lower case path, the root is implicit
	sessions  map[string]*fakeSession
	cursors   map[string][]files.IsMetadata // remaining entries of a listing
	pageSize  int                           // entries per listing page
	maxUpload int                           // bytes per upload request
	counter   int                           // for ids, revisions and cursors
	calls     map[string]int                // number of calls per method
	muted     int                           // number of muted commits
	failNext  map[string][]error            // errors of the next calls of a method, nil for success
}

type fakeEntry struct {
	display  string // path in the case of its creation
	isDir    bool
	data     []byte
	id       string
	rev      string
	modified time.Time
}

type fakeSession struct {
	data   []byte
	closed bool
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		entries:   map[string]*fakeEntry{},
		sessions:  map[string]*fakeSession{},
		cursors:   map[string][]files.IsMetadata{},
		pageSize:  3,
		maxUpload: maxUploadSize,
		calls:     map[string]int{},
		failNext:  map[string][]error{},
	}
}

// begin locks the fake, counts the call of method and returns the error
// of a canceled ctx or the next error set with setFailNext for method.
// The caller must unlock the fake.
func (c *fakeClient) begin(ctx context.Context, method string) error {
	c.mtx.Lock()
	c.calls[method]++
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Post \"https://api.dropboxapi.com/2/files/%s\": %w", method, err)
	}
	if errs := c.failNext[method]; len(errs) > 0 {
		c.failNext[method] = errs[1:]
		return errs[0]
	}
	return nil
}

// callCount returns the number of calls of method.
func (c *fakeClient) callCount(method string) int {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return c.calls[method]
}

// setFailNext makes the next calls of method return errs,
// a nil error lets the call proceed.
func (c *fakeClient) setFailNext(method string, errs ...error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.failNext[method] = errs
}

func (c *fakeClient) nextID() string {
	c.counter++
	return strconv.Itoa(c.counter)
}

func key(p string) string {
	return strings.ToLower(p)
}

// get returns the entry of p, the root as a folder entry.
func (c *fakeClient) get(p string) (*fakeEntry, bool) {
	if p == "" {
		return &fakeEntry{isDir: true}, true
	}
	e, ok := c.entries[key(p)]
	return e, ok
}

func (c *fakeClient) metadata(e *fakeEntry) files.IsMetadata {
	name := path.Base(e.display)
	meta := files.Metadata{Name: name, PathLower: key(e.display), PathDisplay: e.display}
	if e.isDir {
		return &files.FolderMetadata{Metadata: meta, Id: "id:" + e.id}
	}
	return c.fileMetadata(e)
}

func (c *fakeClient) fileMetadata(e *fakeEntry) *files.FileMetadata {
	m := files.NewFileMetadata(path.Base(e.display), "id:"+e.id, dropbox.DBXTime(e.modified), dropbox.DBXTime(e.modified), e.rev, uint64(len(e.data)))
	m.PathLower = key(e.display)
	m.PathDisplay = e.display
	return m
}

func lookupError(tag string) *files.LookupError {
	return &files.LookupError{Tagged: dropbox.Tagged{Tag: tag}}
}

func conflictError(tag string) *files.WriteError {
	return &files.WriteError{
		Tagged:   dropbox.Tagged{Tag: files.WriteErrorConflict},
		Conflict: &files.WriteConflictError{Tagged: dropbox.Tagged{Tag: tag}},
	}
}

func summary(parts ...string) dropbox.APIError {
	return dropbox.APIError{ErrorSummary: strings.Join(parts, "/") + "/"}
}

// lookup checks that p exists and returns its entry,
// or the tag of the LookupError.
func (c *fakeClient) lookup(p string) (*fakeEntry, string) {
	if !strings.HasPrefix(p, "/") && p != "" {
		return nil, files.LookupErrorMalformedPath
	}
	e, ok := c.get(p)
	if !ok {
		return nil, files.LookupErrorNotFound
	}
	return e, ""
}

// prepareWrite checks that p can be written: it creates the missing
// parent folders and returns the tag of a conflict with an existing
// entry at p (file or folder) or a file ancestor, or "".
func (c *fakeClient) prepareWrite(p string) string {
	if p == "" {
		return files.WriteConflictErrorFolder
	}
	var missing []string
	for dir := path.Dir(p); dir != "/"; dir = path.Dir(dir) {
		e, ok := c.entries[key(dir)]
		if !ok {
			missing = append(missing, dir)
			continue
		}
		if !e.isDir {
			return files.WriteConflictErrorFileAncestor
		}
	}
	if e, ok := c.entries[key(p)]; ok {
		if e.isDir {
			return files.WriteConflictErrorFolder
		}
		return files.WriteConflictErrorFile
	}
	for _, dir := range slices.Backward(missing) {
		c.entries[key(dir)] = &fakeEntry{display: dir, isDir: true, id: c.nextID()}
	}
	return ""
}

// commit writes data as file p with mode, see prepareWrite.
func (c *fakeClient) commit(info *files.CommitInfo, data []byte) (*files.FileMetadata, *files.WriteError) {
	conflict := c.prepareWrite(info.Path)
	overwrite := info.Mode != nil && info.Mode.Tag == files.WriteModeOverwrite
	if conflict != "" && (conflict != files.WriteConflictErrorFile || !overwrite) {
		return nil, conflictError(conflict)
	}
	if info.Mute {
		c.muted++
	}
	e, ok := c.entries[key(info.Path)]
	if !ok {
		e = &fakeEntry{display: info.Path, id: c.nextID()}
		c.entries[key(info.Path)] = e
	}
	e.data = bytes.Clone(data)
	e.rev = "rev" + c.nextID()
	e.modified = time.Now().UTC().Truncate(time.Second)
	return c.fileMetadata(e), nil
}

// children returns the metadata of the entries below p,
// only the direct children if recursive is false.
func (c *fakeClient) children(p string, recursive bool) []files.IsMetadata {
	prefix := key(p) + "/"
	var keys []string
	for k := range c.entries {
		if strings.HasPrefix(k, prefix) && (recursive || !strings.Contains(k[len(prefix):], "/")) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	result := make([]files.IsMetadata, len(keys))
	for i, k := range keys {
		result[i] = c.metadata(c.entries[k])
	}
	return result
}

// page returns the first page of entries and stores the rest under a cursor.
func (c *fakeClient) page(entries []files.IsMetadata, limit uint32) *files.ListFolderResult {
	size := c.pageSize
	if limit > 0 && int(limit) < size {
		size = int(limit)
	}
	if len(entries) <= size {
		return files.NewListFolderResult(entries, "", false)
	}
	cursor := "cursor" + c.nextID()
	c.cursors[cursor] = entries[size:]
	return files.NewListFolderResult(entries[:size], cursor, true)
}

func (c *fakeClient) GetMetadataContext(ctx context.Context, arg *files.GetMetadataArg) (files.IsMetadata, error) {
	if err := c.begin(ctx, "GetMetadata"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	e, tag := c.lookup(arg.Path)
	if tag == "" && arg.Path == "" {
		tag = files.LookupErrorMalformedPath // the root has no metadata
	}
	if tag != "" {
		return nil, files.GetMetadataAPIError{
			APIError:      summary("path", tag),
			EndpointError: &files.GetMetadataError{Tagged: dropbox.Tagged{Tag: files.GetMetadataErrorPath}, Path: lookupError(tag)},
		}
	}
	return c.metadata(e), nil
}

func (c *fakeClient) ListFolderContext(ctx context.Context, arg *files.ListFolderArg) (*files.ListFolderResult, error) {
	if err := c.begin(ctx, "ListFolder"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	e, tag := c.lookup(arg.Path)
	if tag == "" && !e.isDir {
		tag = files.LookupErrorNotFolder
	}
	if tag != "" {
		return nil, files.ListFolderAPIError{
			APIError:      summary("path", tag),
			EndpointError: &files.ListFolderError{Tagged: dropbox.Tagged{Tag: files.ListFolderErrorPath}, Path: lookupError(tag)},
		}
	}
	entries := c.children(arg.Path, arg.Recursive)
	if arg.Recursive && arg.Path != "" {
		entries = append([]files.IsMetadata{c.metadata(e)}, entries...)
	}
	return c.page(entries, arg.Limit), nil
}

func (c *fakeClient) ListFolderContinueContext(ctx context.Context, arg *files.ListFolderContinueArg) (*files.ListFolderResult, error) {
	if err := c.begin(ctx, "ListFolderContinue"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	entries, ok := c.cursors[arg.Cursor]
	if !ok {
		return nil, files.ListFolderContinueAPIError{
			APIError:      summary("reset"),
			EndpointError: &files.ListFolderContinueError{Tagged: dropbox.Tagged{Tag: files.ListFolderContinueErrorReset}},
		}
	}
	delete(c.cursors, arg.Cursor)
	return c.page(entries, 0), nil
}

func (c *fakeClient) DownloadContext(ctx context.Context, arg *files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error) {
	if err := c.begin(ctx, "Download"); err != nil {
		defer c.mtx.Unlock()
		return nil, nil, err
	}
	defer c.mtx.Unlock()
	e, tag := c.lookup(arg.Path)
	if tag == "" && e.isDir {
		tag = files.LookupErrorNotFile
	}
	if tag != "" {
		return nil, nil, files.DownloadAPIError{
			APIError:      summary("path", tag),
			EndpointError: &files.DownloadError{Tagged: dropbox.Tagged{Tag: files.DownloadErrorPath}, Path: lookupError(tag)},
		}
	}
	return c.fileMetadata(e), io.NopCloser(bytes.NewReader(bytes.Clone(e.data))), nil
}

func (c *fakeClient) UploadContext(ctx context.Context, arg *files.UploadArg, content io.Reader) (*files.FileMetadata, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	if err := c.begin(ctx, "Upload"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	if len(data) > c.maxUpload {
		return nil, files.UploadAPIError{
			APIError:      summary(files.UploadErrorPayloadTooLarge),
			EndpointError: &files.UploadError{Tagged: dropbox.Tagged{Tag: files.UploadErrorPayloadTooLarge}},
		}
	}
	meta, writeErr := c.commit(&arg.CommitInfo, data)
	if writeErr != nil {
		return nil, files.UploadAPIError{
			APIError: summary("path", "conflict"),
			EndpointError: &files.UploadError{
				Tagged: dropbox.Tagged{Tag: files.UploadErrorPath},
				Path:   &files.UploadWriteFailed{Reason: writeErr},
			},
		}
	}
	return meta, nil
}

func (c *fakeClient) UploadSessionStartContext(ctx context.Context, arg *files.UploadSessionStartArg, content io.Reader) (*files.UploadSessionStartResult, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	if err := c.begin(ctx, "UploadSessionStart"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	if len(data) > c.maxUpload {
		return nil, files.UploadSessionStartAPIError{
			APIError:      summary(files.UploadSessionStartErrorPayloadTooLarge),
			EndpointError: &files.UploadSessionStartError{Tagged: dropbox.Tagged{Tag: files.UploadSessionStartErrorPayloadTooLarge}},
		}
	}
	id := "session" + c.nextID()
	c.sessions[id] = &fakeSession{data: data, closed: arg.Close}
	return files.NewUploadSessionStartResult(id), nil
}

// sessionLookup returns the session of cursor or the tag of
// the UploadSessionLookupError for an unknown session or a wrong offset.
func (c *fakeClient) sessionLookup(cursor *files.UploadSessionCursor) (*fakeSession, string) {
	s, ok := c.sessions[cursor.SessionId]
	switch {
	case !ok:
		return nil, files.UploadSessionLookupErrorNotFound
	case cursor.Offset != uint64(len(s.data)):
		return nil, files.UploadSessionLookupErrorIncorrectOffset
	}
	return s, ""
}

func (c *fakeClient) UploadSessionAppendV2Context(ctx context.Context, arg *files.UploadSessionAppendArg, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	if err := c.begin(ctx, "UploadSessionAppendV2"); err != nil {
		defer c.mtx.Unlock()
		return err
	}
	defer c.mtx.Unlock()
	s, tag := c.sessionLookup(arg.Cursor)
	switch {
	case tag == "" && s.closed:
		tag = files.UploadSessionAppendErrorClosed
	case tag == "" && len(data) > c.maxUpload:
		tag = files.UploadSessionAppendErrorPayloadTooLarge
	}
	if tag != "" {
		return files.UploadSessionAppendV2APIError{
			APIError:      summary(tag),
			EndpointError: &files.UploadSessionAppendError{Tagged: dropbox.Tagged{Tag: tag}},
		}
	}
	s.data = append(s.data, data...)
	s.closed = arg.Close
	return nil
}

func (c *fakeClient) UploadSessionFinishContext(ctx context.Context, arg *files.UploadSessionFinishArg, content io.Reader) (*files.FileMetadata, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	if err := c.begin(ctx, "UploadSessionFinish"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	s, tag := c.sessionLookup(arg.Cursor)
	if tag != "" {
		return nil, files.UploadSessionFinishAPIError{
			APIError: summary("lookup_failed", tag),
			EndpointError: &files.UploadSessionFinishError{
				Tagged:       dropbox.Tagged{Tag: files.UploadSessionFinishErrorLookupFailed},
				LookupFailed: &files.UploadSessionLookupError{Tagged: dropbox.Tagged{Tag: tag}},
			},
		}
	}
	if len(data) > c.maxUpload {
		return nil, files.UploadSessionFinishAPIError{
			APIError:      summary(files.UploadSessionFinishErrorPayloadTooLarge),
			EndpointError: &files.UploadSessionFinishError{Tagged: dropbox.Tagged{Tag: files.UploadSessionFinishErrorPayloadTooLarge}},
		}
	}
	meta, writeErr := c.commit(arg.Commit, append(s.data, data...))
	if writeErr != nil {
		return nil, files.UploadSessionFinishAPIError{
			APIError:      summary("path", "conflict"),
			EndpointError: &files.UploadSessionFinishError{Tagged: dropbox.Tagged{Tag: files.UploadSessionFinishErrorPath}, Path: writeErr},
		}
	}
	delete(c.sessions, arg.Cursor.SessionId)
	return meta, nil
}

func (c *fakeClient) CreateFolderV2Context(ctx context.Context, arg *files.CreateFolderArg) (*files.CreateFolderResult, error) {
	if err := c.begin(ctx, "CreateFolderV2"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	if conflict := c.prepareWrite(arg.Path); conflict != "" {
		return nil, files.CreateFolderV2APIError{
			APIError:      summary("path", "conflict", conflict),
			EndpointError: &files.CreateFolderError{Tagged: dropbox.Tagged{Tag: files.CreateFolderErrorPath}, Path: conflictError(conflict)},
		}
	}
	e := &fakeEntry{display: arg.Path, isDir: true, id: c.nextID()}
	c.entries[key(arg.Path)] = e
	return files.NewCreateFolderResult(c.metadata(e).(*files.FolderMetadata)), nil
}

// relocationError returns the error of a failed MoveV2 or CopyV2.
func relocationError(move bool, r *files.RelocationError) error {
	api := summary(r.Tag)
	if move {
		return files.MoveV2APIError{APIError: api, EndpointError: r}
	}
	return files.CopyV2APIError{APIError: api, EndpointError: r}
}

// relocate implements MoveV2 and CopyV2.
func (c *fakeClient) relocate(ctx context.Context, move bool, arg *files.RelocationArg) (*files.RelocationResult, error) {
	method := "CopyV2"
	if move {
		method = "MoveV2"
	}
	if err := c.begin(ctx, method); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	from, to := arg.FromPath, arg.ToPath
	src, tag := c.lookup(from)
	if tag == "" && from == "" {
		tag = files.LookupErrorMalformedPath
	}
	if tag != "" {
		return nil, relocationError(move, &files.RelocationError{
			Tagged:     dropbox.Tagged{Tag: files.RelocationErrorFromLookup},
			FromLookup: lookupError(tag),
		})
	}
	if src.isDir && strings.HasPrefix(key(to), key(from)+"/") {
		tag := files.RelocationErrorDuplicatedOrNestedPaths
		if move {
			tag = files.RelocationErrorCantMoveFolderIntoItself
		}
		return nil, relocationError(move, &files.RelocationError{Tagged: dropbox.Tagged{Tag: tag}})
	}
	if key(from) == key(to) {
		// Like a path that is in the way, Dropbox
		// doesn't support case-only renames
		return nil, relocationError(move, &files.RelocationError{
			Tagged: dropbox.Tagged{Tag: files.RelocationErrorTo},
			To:     conflictError(files.WriteConflictErrorFile),
		})
	}
	if conflict := c.prepareWrite(to); conflict != "" {
		return nil, relocationError(move, &files.RelocationError{
			Tagged: dropbox.Tagged{Tag: files.RelocationErrorTo},
			To:     conflictError(conflict),
		})
	}
	// Relocate the entry and everything below it
	fromKey := key(from)
	for k, e := range c.entries {
		if k != fromKey && !strings.HasPrefix(k, fromKey+"/") {
			continue
		}
		moved := *e
		moved.display = to + e.display[len(from):]
		if !move {
			moved.id = c.nextID()
			moved.data = bytes.Clone(e.data)
		}
		if move {
			delete(c.entries, k)
		}
		c.entries[key(moved.display)] = &moved
	}
	moved, _ := c.get(to)
	return files.NewRelocationResult(c.metadata(moved)), nil
}

func (c *fakeClient) MoveV2Context(ctx context.Context, arg *files.RelocationArg) (*files.RelocationResult, error) {
	return c.relocate(ctx, true, arg)
}

func (c *fakeClient) CopyV2Context(ctx context.Context, arg *files.RelocationArg) (*files.RelocationResult, error) {
	return c.relocate(ctx, false, arg)
}

func (c *fakeClient) DeleteV2Context(ctx context.Context, arg *files.DeleteArg) (*files.DeleteResult, error) {
	if err := c.begin(ctx, "DeleteV2"); err != nil {
		defer c.mtx.Unlock()
		return nil, err
	}
	defer c.mtx.Unlock()
	e, tag := c.lookup(arg.Path)
	if tag == "" && arg.Path == "" {
		tag = files.LookupErrorMalformedPath
	}
	if tag != "" {
		return nil, files.DeleteV2APIError{
			APIError:      summary("path_lookup", tag),
			EndpointError: &files.DeleteError{Tagged: dropbox.Tagged{Tag: files.DeleteErrorPathLookup}, PathLookup: lookupError(tag)},
		}
	}
	if arg.ParentRev != "" && (e.isDir || e.rev != arg.ParentRev) {
		return nil, files.DeleteV2APIError{
			APIError:      summary("path_write", "conflict"),
			EndpointError: &files.DeleteError{Tagged: dropbox.Tagged{Tag: files.DeleteErrorPathWrite}, PathWrite: conflictError(files.WriteConflictErrorFile)},
		}
	}
	meta := c.metadata(e)
	k := key(arg.Path)
	for other := range c.entries {
		if other == k || strings.HasPrefix(other, k+"/") {
			delete(c.entries, other)
		}
	}
	return files.NewDeleteResult(meta), nil
}
