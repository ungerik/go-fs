// Package gittest has an in-memory git repository and a fake git server
// for the offline tests of gitfs. The server speaks git protocol version 2
// over smart HTTP with the object filters that gitfs uses, and fakes the
// endpoints of the GitHub REST API and raw.githubusercontent.com.
package gittest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ungerik/go-fs/gitfs/internal/object"
	"github.com/ungerik/go-fs/gitfs/internal/pack"
	"github.com/ungerik/go-fs/gitfs/internal/pktline"
)

// Repo is an in-memory git repository.
type Repo struct {
	Format  object.Format
	Objects map[object.OID]*pack.Object
	Refs    map[string]object.OID // by full ref name
	Head    string                // the ref that HEAD points to, empty for no HEAD
}

// NewRepo returns an empty sha1 repository
// whose HEAD points to refs/heads/main.
func NewRepo() *Repo {
	return &Repo{
		Format:  object.SHA1,
		Objects: make(map[object.OID]*pack.Object),
		Refs:    make(map[string]object.OID),
		Head:    "refs/heads/main",
	}
}

// Add adds an object and returns its ID.
func (r *Repo) Add(typ object.Type, data []byte) object.OID {
	oid := r.Format.Hash(typ, data)
	r.Objects[oid] = &pack.Object{Type: typ, Data: data}
	return oid
}

// File is the content of a file of a tree.
type File struct {
	Content string
	// Mode is object.ModeFile if zero. The content of an object.ModeSymlink
	// is the target, the content of an object.ModeSubmodule a commit ID.
	Mode uint32
}

// Tree adds the trees of the files by slash separated path
// and returns the ID of the root tree.
func (r *Repo) Tree(files map[string]File) object.OID {
	direct := make(map[string]File)
	subdirs := make(map[string]map[string]File)
	for path, file := range files {
		dir, rest, isNested := strings.Cut(path, "/")
		if !isNested {
			direct[path] = file
			continue
		}
		if subdirs[dir] == nil {
			subdirs[dir] = make(map[string]File)
		}
		subdirs[dir][rest] = file
	}
	var entries []object.TreeEntry
	for name, file := range direct {
		mode := file.Mode
		if mode == 0 {
			mode = object.ModeFile
		}
		var oid object.OID
		switch mode {
		case object.ModeSubmodule:
			oid = object.OID(file.Content)
		default:
			oid = r.Add(object.TypeBlob, []byte(file.Content))
		}
		entries = append(entries, object.TreeEntry{Mode: mode, Name: name, OID: oid})
	}
	for name, sub := range subdirs {
		entries = append(entries, object.TreeEntry{Mode: object.ModeDir, Name: name, OID: r.Tree(sub)})
	}
	return r.RawTree(entries...)
}

// RawTree adds a tree with the entries as passed, also invalid ones.
func (r *Repo) RawTree(entries ...object.TreeEntry) object.OID {
	slices.SortFunc(entries, func(a, b object.TreeEntry) int { return strings.Compare(a.Name, b.Name) })
	var data []byte
	for _, e := range entries {
		raw, err := e.OID.Raw()
		if err != nil {
			panic(err)
		}
		data = fmt.Appendf(data, "%o %s\x00", e.Mode, e.Name)
		data = append(data, raw...)
	}
	return r.Add(object.TypeTree, data)
}

// Commit adds a commit of the tree with the committer time.
func (r *Repo) Commit(tree object.OID, committed time.Time, parents ...object.OID) object.OID {
	data := fmt.Appendf(nil, "tree %s\n", tree)
	for _, parent := range parents {
		data = fmt.Appendf(data, "parent %s\n", parent)
	}
	signature := fmt.Sprintf("Test <test@example.com> %d %s", committed.Unix(), committed.Format("-0700"))
	data = fmt.Appendf(data, "author %s\ncommitter %s\n\ncommit\n", signature, signature)
	return r.Add(object.TypeCommit, data)
}

// Tag adds an annotated tag of the target and the ref refs/tags/name.
func (r *Repo) Tag(name string, target object.OID) object.OID {
	typ := r.Objects[target].Type
	data := fmt.Appendf(nil, "object %s\ntype %s\ntag %s\ntagger Test <test@example.com> 0 +0000\n\ntag\n", target, typ, name)
	oid := r.Add(object.TypeTag, data)
	r.Refs["refs/tags/"+name] = oid
	return oid
}

// peel returns the object that an annotated tag points to, else oid.
func (r *Repo) peel(oid object.OID) object.OID {
	for {
		obj := r.Objects[oid]
		if obj == nil || obj.Type != object.TypeTag {
			return oid
		}
		tag, err := r.Format.ParseTag(obj.Data)
		if err != nil {
			panic(err)
		}
		oid = tag.Object
	}
}

// entries returns the entries of a tree object.
func (r *Repo) entries(tree object.OID) []object.TreeEntry {
	entries, err := r.Format.ParseTree(r.Objects[tree].Data)
	if err != nil {
		panic(err)
	}
	return entries
}

// Server is a fake git server for one repository at the path
// "/owner/repo.git", with the GitHub REST API below "/api" and
// raw.githubusercontent.com below "/raw".
type Server struct {
	Repo *Repo

	// NoFilter disables the filter feature of fetch.
	NoFilter bool
	// ProtocolV0 makes the server answer with protocol version 0.
	ProtocolV0 bool
	// Dumb makes the server a dumb HTTP server.
	Dumb bool
	// Username and Password are required with Basic authentication if set.
	Username, Password string
	// FetchError is sent on side-band channel 3 by fetch if set.
	FetchError string
	// GitHubDown makes the GitHub REST API answer 403 like over the rate limit.
	GitHubDown bool
	// RawDown makes raw.githubusercontent.com answer 404
	// like for a private repository without token.
	RawDown bool
	// RawContent replaces the content that raw.githubusercontent.com
	// serves for the paths, like the content of a Git LFS file.
	RawContent map[string]string
	// BeforeFetch is called before a fetch command is answered if set,
	// with the context of the request.
	BeforeFetch func(ctx context.Context)

	mtx      sync.Mutex
	requests map[string]int
}

// Requests returns the number of requests by kind:
// "info/refs", "ls-refs", "fetch", "api" and "raw".
func (s *Server) Requests() map[string]int {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return maps.Clone(s.requests)
}

// ResetRequests resets the request counts.
func (s *Server) ResetRequests() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	clear(s.requests)
}

func (s *Server) count(kind string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.requests == nil {
		s.requests = make(map[string]int)
	}
	s.requests[kind]++
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/repos/owner/repo/git/trees/"):
		s.count("api")
		s.serveGitHubTree(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/raw/owner/repo/"):
		s.count("raw")
		s.serveRaw(w, r)
		return
	}
	if s.Username != "" {
		username, password, ok := r.BasicAuth()
		if !ok || username != s.Username || password != s.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/owner/repo.git/info/refs":
		s.count("info/refs")
		s.serveAdvertisement(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/owner/repo.git/git-upload-pack":
		s.serveUploadPack(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveAdvertisement(w http.ResponseWriter, r *http.Request) {
	if s.Dumb || r.URL.Query().Get("service") != "git-upload-pack" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("dumb"))
		return
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	var e pktline.Encoder
	e.Line("# service=git-upload-pack")
	e.Flush()
	if s.ProtocolV0 || r.Header.Get("Git-Protocol") != "version=2" {
		e.Packet([]byte(string(s.Repo.Refs[s.Repo.Head]) + " HEAD\x00multi_ack\n"))
		e.Flush()
		_, _ = w.Write(e.Bytes())
		return
	}
	e.Line("version 2")
	e.Line("agent=gittest")
	e.Line("ls-refs=unborn")
	if s.NoFilter {
		e.Line("fetch=shallow")
	} else {
		e.Line("fetch=shallow filter")
	}
	e.Line("object-format=" + s.Repo.Format.Name)
	e.Flush()
	_, _ = w.Write(e.Bytes())
}

// readCommand reads the command and the arguments of a request.
func readCommand(r *http.Request) (command string, args []string, err error) {
	d := pktline.NewDecoder(r.Body)
	for {
		kind, line, err := d.NextLine()
		if err != nil {
			return "", nil, err
		}
		switch kind {
		case pktline.Flush:
			return command, args, nil
		case pktline.Delim:
			continue
		}
		if c, ok := strings.CutPrefix(line, "command="); ok {
			command = c
			continue
		}
		args = append(args, line)
	}
}

func (s *Server) serveUploadPack(w http.ResponseWriter, r *http.Request) {
	command, args, err := readCommand(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	switch command {
	case "ls-refs":
		s.count("ls-refs")
		s.serveLsRefs(w, args)
	case "fetch":
		s.count("fetch")
		if s.BeforeFetch != nil {
			s.BeforeFetch(r.Context())
		}
		s.serveFetch(w, args)
	default:
		http.Error(w, "unknown command "+command, http.StatusBadRequest)
	}
}

func (s *Server) serveLsRefs(w http.ResponseWriter, args []string) {
	var prefixes []string
	peel, symrefs := false, false
	for _, arg := range args {
		switch {
		case arg == "peel":
			peel = true
		case arg == "symrefs":
			symrefs = true
		case strings.HasPrefix(arg, "ref-prefix "):
			prefixes = append(prefixes, strings.TrimPrefix(arg, "ref-prefix "))
		}
	}
	refs := maps.Clone(s.Repo.Refs)
	if s.Repo.Head != "" && refs[s.Repo.Head] != "" {
		refs["HEAD"] = refs[s.Repo.Head]
	}
	var e pktline.Encoder
	for _, name := range slices.Sorted(maps.Keys(refs)) {
		if len(prefixes) > 0 && !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		line := string(refs[name]) + " " + name
		if name == "HEAD" && symrefs {
			line += " symref-target:" + s.Repo.Head
		}
		if peeled := s.Repo.peel(refs[name]); peel && peeled != refs[name] {
			line += " peeled:" + string(peeled)
		}
		e.Line(line)
	}
	e.Flush()
	_, _ = w.Write(e.Bytes())
}

func (s *Server) serveFetch(w http.ResponseWriter, args []string) {
	var (
		wants  []object.OID
		filter string
		deepen bool
	)
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "want "):
			wants = append(wants, object.OID(strings.TrimPrefix(arg, "want ")))
		case strings.HasPrefix(arg, "filter "):
			filter = strings.TrimPrefix(arg, "filter ")
		case strings.HasPrefix(arg, "deepen "):
			deepen = true
		}
	}
	var e pktline.Encoder
	for _, want := range wants {
		if s.Repo.Objects[want] == nil {
			e.Line("ERR upload-pack: not our ref " + string(want))
			_, _ = w.Write(e.Bytes())
			return
		}
	}
	if filter != "" && filter != "tree:0" && filter != "blob:none" {
		e.Line("ERR unsupported filter " + filter)
		_, _ = w.Write(e.Bytes())
		return
	}

	// Collect the wanted objects and what they reach, without the
	// parents of commits: gitfs always fetches with deepen 1
	included := make(map[object.OID]bool)
	var order []object.OID
	var walk func(oid object.OID, wanted bool)
	walk = func(oid object.OID, wanted bool) {
		obj := s.Repo.Objects[oid]
		if obj == nil || included[oid] {
			return // submodule commits are not part of the repository
		}
		switch {
		case wanted:
		case filter == "tree:0" && obj.Type != object.TypeCommit:
			return
		case filter == "blob:none" && obj.Type == object.TypeBlob:
			return
		}
		included[oid] = true
		order = append(order, oid)
		switch obj.Type {
		case object.TypeTag:
			walk(s.Repo.peel(oid), false)
		case object.TypeCommit:
			if filter == "tree:0" {
				return
			}
			commit, err := s.Repo.Format.ParseCommit(obj.Data)
			if err != nil {
				panic(err)
			}
			walk(commit.Tree, false)
		case object.TypeTree:
			if filter == "tree:0" {
				return
			}
			for _, entry := range s.Repo.entries(oid) {
				if entry.Mode != object.ModeSubmodule {
					walk(entry.OID, false)
				}
			}
		}
	}
	for _, want := range wants {
		walk(want, true)
	}
	objects := make([]*pack.Object, len(order))
	for i, oid := range order {
		objects[i] = s.Repo.Objects[oid]
	}

	if deepen {
		e.Line("shallow-info")
		for _, want := range wants {
			if s.Repo.Objects[want].Type == object.TypeCommit {
				e.Line("shallow " + string(want))
			}
		}
		e.Delim()
	}
	e.Line("packfile")
	var packfile bytes.Buffer
	err := pack.Write(&packfile, s.Repo.Format, objects)
	if err != nil {
		panic(err)
	}
	if s.FetchError != "" {
		e.Packet(append([]byte{3}, s.FetchError...))
	} else {
		for chunk := range slices.Chunk(packfile.Bytes(), 8192) {
			e.Packet(append([]byte{2}, "progress\n"...))
			e.Packet(append([]byte{1}, chunk...))
		}
	}
	e.Flush()
	_, _ = w.Write(e.Bytes())
}

// lookup returns the tree entry of a slash separated path in a commit.
func (s *Server) lookup(commit object.OID, path string) (object.TreeEntry, bool) {
	obj := s.Repo.Objects[commit]
	if obj == nil || obj.Type != object.TypeCommit {
		return object.TreeEntry{}, false
	}
	c, err := s.Repo.Format.ParseCommit(obj.Data)
	if err != nil {
		panic(err)
	}
	current := object.TreeEntry{Mode: object.ModeDir, OID: c.Tree}
	for element := range strings.SplitSeq(path, "/") {
		if current.Mode != object.ModeDir {
			return object.TreeEntry{}, false
		}
		i := slices.IndexFunc(s.Repo.entries(current.OID), func(e object.TreeEntry) bool { return e.Name == element })
		if i < 0 {
			return object.TreeEntry{}, false
		}
		current = s.Repo.entries(current.OID)[i]
	}
	return current, true
}

func (s *Server) serveRaw(w http.ResponseWriter, r *http.Request) {
	commit, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/raw/owner/repo/"), "/")
	if s.RawDown {
		http.NotFound(w, r)
		return
	}
	if content, ok := s.RawContent[path]; ok {
		_, _ = w.Write([]byte(content))
		return
	}
	entry, ok := s.lookup(object.OID(commit), path)
	if !ok || entry.Mode == object.ModeDir || entry.Mode == object.ModeSubmodule {
		http.NotFound(w, r)
		return
	}
	// Like raw.githubusercontent.com, the content of a symbolic link is its target
	_, _ = w.Write(s.Repo.Objects[entry.OID].Data)
}

func (s *Server) serveGitHubTree(w http.ResponseWriter, r *http.Request) {
	if s.GitHubDown {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		return
	}
	tree := object.OID(strings.TrimPrefix(r.URL.Path, "/api/repos/owner/repo/git/trees/"))
	if obj := s.Repo.Objects[tree]; obj == nil || obj.Type != object.TypeTree {
		http.NotFound(w, r)
		return
	}
	type entry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
		Size *int64 `json:"size,omitempty"`
	}
	var entries []entry
	var walk func(prefix string, tree object.OID)
	walk = func(prefix string, tree object.OID) {
		for _, e := range s.Repo.entries(tree) {
			out := entry{Path: prefix + e.Name, Mode: fmt.Sprintf("%06o", e.Mode), SHA: string(e.OID)}
			switch e.Mode {
			case object.ModeDir:
				out.Type = "tree"
			case object.ModeSubmodule:
				out.Type = "commit"
			default:
				out.Type = "blob"
				size := int64(len(s.Repo.Objects[e.OID].Data))
				out.Size = &size
			}
			entries = append(entries, out)
			if e.Mode == object.ModeDir && r.URL.Query().Get("recursive") != "" {
				walk(prefix+e.Name+"/", e.OID)
			}
		}
	}
	walk("", tree)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sha": tree, "tree": entries, "truncated": false})
}
