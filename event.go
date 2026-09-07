package fs

import "github.com/fsnotify/fsnotify"

// Event reported for watched files.
//
// This is a bitmask and some systems may send multiple operations at once.
// Use the Has... methods to check if an event has a certain operation.
type Event uint32

// String returns the names of the operations in the event bitmask.
func (e Event) String() string { return fsnotify.Op(e).String() }

// HasCreate reports if the event includes the creation of the file.
func (e Event) HasCreate() bool { return fsnotify.Op(e).Has(fsnotify.Create) }

// HasWrite reports if the event includes a write to the file.
func (e Event) HasWrite() bool { return fsnotify.Op(e).Has(fsnotify.Write) }

// HasRemove reports if the event includes the removal of the file.
func (e Event) HasRemove() bool { return fsnotify.Op(e).Has(fsnotify.Remove) }

// HasRename reports if the event includes the renaming of the file.
func (e Event) HasRename() bool { return fsnotify.Op(e).Has(fsnotify.Rename) }

// HasChmod reports if the event includes a change of the file's
// permissions or attributes.
func (e Event) HasChmod() bool { return fsnotify.Op(e).Has(fsnotify.Chmod) }

// Used only for testing
const (
	eventCreate = Event(fsnotify.Create)
	// eventWrite  = Event(fsnotify.Write)
	eventRemove = Event(fsnotify.Remove)
	eventRename = Event(fsnotify.Rename)
	// eventChmod  = Event(fsnotify.Chmod)
)
