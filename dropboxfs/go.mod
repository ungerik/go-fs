module github.com/ungerik/go-fs/dropboxfs

go 1.23

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

// External
require github.com/tj/go-dropbox v0.0.0-20171107035848-42dd2be3662d

require (
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	github.com/segmentio/go-env v1.1.0 // indirect
	github.com/ungerik/go-dry v0.0.0-20231011182423-d9a07fd18c5f // indirect
	golang.org/x/sys v0.24.0 // indirect
)
