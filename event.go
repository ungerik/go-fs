package fs

import "strings"

// Event reported for watched files.
//
// This is a bitmask and some systems may send multiple operations at once.
// Use the Has... methods to check if an event has a certain operation.
type Event uint32

const (
	// EventCreate is reported when a file was created.
	EventCreate Event = 1 << iota

	// EventWrite is reported when a file was written to.
	EventWrite

	// EventRemove is reported when a file was removed.
	EventRemove

	// EventRename is reported when a file was renamed.
	EventRename

	// EventChmod is reported when the permissions
	// or attributes of a file changed.
	EventChmod
)

// String returns the names of the operations in the event bitmask
// joined with "|", for example "CREATE|WRITE".
func (e Event) String() string {
	var names []string
	for _, op := range []struct {
		event Event
		name  string
	}{
		{EventCreate, "CREATE"},
		{EventWrite, "WRITE"},
		{EventRemove, "REMOVE"},
		{EventRename, "RENAME"},
		{EventChmod, "CHMOD"},
	} {
		if e.has(op.event) {
			names = append(names, op.name)
		}
	}
	if len(names) == 0 {
		return "[no events]"
	}
	return strings.Join(names, "|")
}

func (e Event) has(op Event) bool { return e&op == op }

// HasCreate reports if the event includes the creation of the file.
func (e Event) HasCreate() bool { return e.has(EventCreate) }

// HasWrite reports if the event includes a write to the file.
func (e Event) HasWrite() bool { return e.has(EventWrite) }

// HasRemove reports if the event includes the removal of the file.
func (e Event) HasRemove() bool { return e.has(EventRemove) }

// HasRename reports if the event includes the renaming of the file.
func (e Event) HasRename() bool { return e.has(EventRename) }

// HasChmod reports if the event includes a change of the file's
// permissions or attributes.
func (e Event) HasChmod() bool { return e.has(EventChmod) }
