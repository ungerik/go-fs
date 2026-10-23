package fs

import (
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
)

func TestEvent_String(t *testing.T) {
	assert.Equal(t, "[no events]", Event(0).String())
	assert.Equal(t, "CREATE", EventCreate.String())
	assert.Equal(t, "CREATE|WRITE", (EventCreate | EventWrite).String())
	assert.Equal(t, "REMOVE|RENAME|CHMOD", (EventChmod | EventRename | EventRemove).String(), "the names are in a fixed order")
	assert.Equal(t, "CREATE|WRITE|REMOVE|RENAME|CHMOD", (EventCreate | EventWrite | EventRemove | EventRename | EventChmod).String())
}

func TestEvent_Has(t *testing.T) {
	for _, tt := range []struct {
		event                                Event
		create, write, remove, rename, chmod bool
	}{
		{event: 0},
		{event: EventCreate, create: true},
		{event: EventWrite, write: true},
		{event: EventRemove, remove: true},
		{event: EventRename, rename: true},
		{event: EventChmod, chmod: true},
		{event: EventCreate | EventWrite, create: true, write: true},
		{event: EventRename | EventChmod, rename: true, chmod: true},
	} {
		assert.Equal(t, tt.create, tt.event.HasCreate(), "%s HasCreate", tt.event)
		assert.Equal(t, tt.write, tt.event.HasWrite(), "%s HasWrite", tt.event)
		assert.Equal(t, tt.remove, tt.event.HasRemove(), "%s HasRemove", tt.event)
		assert.Equal(t, tt.rename, tt.event.HasRename(), "%s HasRename", tt.event)
		assert.Equal(t, tt.chmod, tt.event.HasChmod(), "%s HasChmod", tt.event)
	}
}

// TestEventFromFSNotify checks that every operation of an fsnotify event
// is mapped, also when an event has several of them.
func TestEventFromFSNotify(t *testing.T) {
	assert.Equal(t, Event(0), eventFromFSNotify(0))
	assert.Equal(t, EventCreate, eventFromFSNotify(fsnotify.Create))
	assert.Equal(t, EventWrite, eventFromFSNotify(fsnotify.Write))
	assert.Equal(t, EventRemove, eventFromFSNotify(fsnotify.Remove))
	assert.Equal(t, EventRename, eventFromFSNotify(fsnotify.Rename))
	assert.Equal(t, EventChmod, eventFromFSNotify(fsnotify.Chmod))
	assert.Equal(t, EventChmod|EventRename, eventFromFSNotify(fsnotify.Chmod|fsnotify.Rename))
	assert.Equal(t, EventCreate|EventWrite|EventRemove|EventRename|EventChmod,
		eventFromFSNotify(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod))
}
