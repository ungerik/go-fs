package fs

import "github.com/fsnotify/fsnotify"

// Event reported for watched files.
//
// This is a bitmask and some systems may send multiple operations at once.
// Use the Has... methods to check if an event has a certain operation.
type Event int

func (e Event) String() string  { return fsnotify.Op(e).String() }
func (e Event) HasCreate() bool { return fsnotify.Op(e).Has(fsnotify.Create) }
func (e Event) HasWrite() bool  { return fsnotify.Op(e).Has(fsnotify.Write) }
func (e Event) HasRemove() bool { return fsnotify.Op(e).Has(fsnotify.Remove) }
func (e Event) HasRename() bool { return fsnotify.Op(e).Has(fsnotify.Rename) }
func (e Event) HasChmod() bool  { return fsnotify.Op(e).Has(fsnotify.Chmod) }

// Used only for testing
const (
	eventCreate = Event(fsnotify.Create)
	// eventWrite  = Event(fsnotify.Write)
	eventRemove = Event(fsnotify.Remove)
	eventRename = Event(fsnotify.Rename)
	// eventChmod  = Event(fsnotify.Chmod)
)
