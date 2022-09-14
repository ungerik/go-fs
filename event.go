package fs

import "github.com/fsnotify/fsnotify"

type Event int

func (t Event) String() string {
	return fsnotify.Op(t).String()
}

const (
	EventCreate = Event(fsnotify.Create)
	EventWrite  = Event(fsnotify.Write)
	EventRemove = Event(fsnotify.Remove)
	EventRename = Event(fsnotify.Rename)
	EventChmod  = Event(fsnotify.Chmod)
)
