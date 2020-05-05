package zipfs

import (
	"path"
	"sort"
	"time"

	"github.com/ungerik/go-fs"
)

type node struct {
	filePath string
	fs.FileInfo
	children map[string]*node
}

func (n *node) hasChild(name string) bool {
	return n.children != nil && n.children[name] != nil
}

// sortedChildren returns the children sorted with directories first
// and directories and files sorted by name
func (n *node) sortedChildren() []*node {
	l := len(n.children)
	if l == 0 {
		return nil
	}
	s := make([]*node, l)
	i := 0
	for _, n := range n.children {
		s[i] = n
		i++
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].IsDir() != s[j].IsDir() {
			return s[i].IsDir()
		}
		return s[i].Name() < s[j].Name()
	})
	return s
}

func (n *node) sortedChildDirs() []*node {
	l := len(n.children)
	if l == 0 {
		return nil
	}
	s := make([]*node, 0, l)
	for _, n := range n.children {
		if n.IsDir() {
			s = append(s, n)
		}
	}
	sort.Slice(s, func(i, j int) bool {
		return s[i].Name() < s[j].Name()
	})
	return s
}

// func (n *node) addChild(info fs.FileInfo) {
// 	child := &node{FileInfo: info}
// 	if info.IsDir() {
// 		child.children = make(map[string]*node)
// 	}
// 	n.children[info.Name()] = child
// }

func (n *node) addChildDir(filePath string, modTime time.Time) (child *node) {
	name := path.Base(filePath)
	child = n.children[name]
	if child != nil {
		if !child.IsDir() {
			panic("existing child is not a directory")
		}
		return child
	}
	child = &node{
		filePath: filePath,
		FileInfo: fs.FileInfo{
			Name:        name,
			Exists:      true,
			IsDir:       true,
			IsRegular:   true,
			IsHidden:    name[0] == '.',
			Size:        0,
			ModTime:     modTime,
			Permissions: fs.AllRead,
		},
		children: make(map[string]*node),
	}
	n.children[name] = child
	return child
}

func (n *node) addChildFile(filePath string, modTime time.Time, size int64) (child *node) {
	name := path.Base(filePath)
	child = n.children[name]
	if child != nil {
		if child.IsDir() {
			panic("existing child is a directory")
		}
		return child
	}
	child = &node{
		filePath: filePath,
		FileInfo: fs.FileInfo{
			Name:        name,
			Exists:      true,
			IsDir:       false,
			IsRegular:   true,
			IsHidden:    name[0] == '.',
			Size:        size,
			ModTime:     modTime,
			Permissions: fs.AllRead,
		},
	}
	n.children[name] = child
	return child
}
