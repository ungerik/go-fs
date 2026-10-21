// Package dirtree indexes the flat entry names of an archive as a
// directory tree, with the directories implied by the entry paths.
//
// It is used by the readers of tarfs and zipfs to look up and list
// entries, and by their writers to detect entries that were already
// written or conflict with a written one.
package dirtree

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

// Node is a file or directory of a Tree.
type Node struct {
	Path     string // slash separated path without leading slash, "" for the root
	Name     string // last element of Path, "" for the root
	IsDir    bool
	Size     int64     // size of a file, zero for directories
	Modified time.Time // zero if unknown
	Sys      any       // data of the archive implementation, nil for implied directories

	children map[string]*Node // nil for files
}

// Tree is a directory tree of archive entries, create it with New.
// Concurrent reads are safe, modifications need exclusive access.
type Tree struct {
	root Node
}

// New returns an empty tree that only has its root directory.
func New() *Tree {
	return &Tree{root: Node{IsDir: true}}
}

// Root returns the root directory of the tree.
func (t *Tree) Root() *Node {
	return &t.root
}

// CleanName returns an entry name of an archive as a valid, slash
// separated path without leading slash and without "." or ".."
// elements, like archive/zip does for its io/fs view:
// backslashes become slashes and ".." elements that would leave
// the root are removed, so `..\..\evil.txt` becomes "evil.txt".
// An empty string is returned for names of the root.
func CleanName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

// Elements returns the path elements of a slash separated entry name.
// A leading or trailing slash and empty or "." elements are ignored.
// It returns an error wrapping os.ErrInvalid for names that contain
// a backslash or a ".." element, because those could address a path
// outside of the archive when the entries are extracted.
func Elements(name string) ([]string, error) {
	if strings.Contains(name, `\`) {
		return nil, fmt.Errorf("%w: archive entry name %q must not contain a backslash", os.ErrInvalid, name)
	}
	var elements []string
	for element := range strings.SplitSeq(name, "/") {
		switch element {
		case "", ".":
			continue
		case "..":
			return nil, fmt.Errorf("%w: archive entry name %q must not contain a %q element", os.ErrInvalid, name, "..")
		}
		elements = append(elements, element)
	}
	return elements, nil
}

// Add adds the entry name to the tree and returns its node,
// creating the directories along its path as implied directories.
// See Elements for the entry names that are rejected with an error.
// A file can't have the name of the root.
//
// A later entry replaces an earlier one with the same path, the way
// extracting an archive overwrites earlier files: a file replaces a
// file or a directory together with its children, a directory replaces
// a file, and a directory entry for an existing directory keeps the
// children and updates Modified and Sys. A file in the way of a later
// entry's path is replaced by an implied directory.
func (t *Tree) Add(name string, isDir bool, size int64, modified time.Time, sys any) (*Node, error) {
	elements, err := Elements(name)
	if err != nil {
		return nil, err
	}
	if len(elements) == 0 && !isDir {
		return nil, fmt.Errorf("archive file entry %q has no name", name)
	}
	current := t.Root()
	for i, element := range elements {
		last := i == len(elements)-1
		child := current.children[element]
		switch {
		case last && !isDir:
			child = &Node{Name: element, Size: size}
		case child == nil || !child.IsDir:
			child = &Node{Name: element, IsDir: true, Modified: modified}
		}
		if current.children == nil {
			current.children = make(map[string]*Node)
		}
		child.Path = strings.Join(elements[:i+1], "/")
		current.children[element] = child
		current = child
	}
	current.Modified = modified
	current.Sys = sys
	return current, nil
}

// Lookup returns the node at the slash separated path or nil.
// Empty and "." elements of path are ignored.
func (t *Tree) Lookup(path string) *Node {
	current := t.Root()
	for element := range strings.SplitSeq(path, "/") {
		if element == "" || element == "." {
			continue
		}
		child, ok := current.children[element]
		if !ok {
			return nil
		}
		current = child
	}
	return current
}

// Conflict returns the node that keeps a new entry name from being
// added without replacing anything: the node at name itself, which
// is the root for an empty name, or else the nearest ancestor of name
// that is a file, with isAncestor true. It returns nil if there is no
// such node, and an error for an invalid name, see Elements.
func (t *Tree) Conflict(name string) (node *Node, isAncestor bool, err error) {
	elements, err := Elements(name)
	if err != nil {
		return nil, false, err
	}
	current := t.Root()
	for _, element := range elements {
		if !current.IsDir {
			return current, true, nil
		}
		child, ok := current.children[element]
		if !ok {
			return nil, false, nil
		}
		current = child
	}
	return current, false, nil
}

// Children returns the children of a directory node
// sorted by name with directories first.
func (n *Node) Children() []*Node {
	children := make([]*Node, 0, len(n.children))
	for _, child := range n.children {
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].IsDir != children[j].IsDir {
			return children[i].IsDir
		}
		return children[i].Name < children[j].Name
	})
	return children
}

// List calls callback for the children of the directory node dir whose
// names match any of the patterns, or for all of them if no patterns are
// passed, see fsimpl.MatchAnyPattern. With recursive, the directories
// are descended into instead of being passed to callback, so only the
// files below dir are listed. Children are listed in the order of
// Children. The ctx is checked before every callback.
func List(ctx context.Context, dir *Node, patterns []string, recursive bool, callback func(*Node) error) error {
	for _, child := range dir.Children() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if recursive && child.IsDir {
			if err := List(ctx, child, patterns, recursive, callback); err != nil {
				return err
			}
			continue
		}
		match, err := fsimpl.MatchAnyPattern(child.Name, patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		if err := callback(child); err != nil {
			return err
		}
	}
	return nil
}
