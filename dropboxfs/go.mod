module github.com/ungerik/go-fs/dropboxfs

go 1.19

// Parent module in same repo
require github.com/ungerik/go-fs v0.0.0-20230126150009-0ad15068f049

replace github.com/ungerik/go-fs => ../

// External
require github.com/tj/go-dropbox v0.0.0-20171107035848-42dd2be3662d

require (
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/segmentio/go-env v1.1.0 // indirect
	github.com/ungerik/go-dry v0.0.0-20220205124545-c028a5f03370 // indirect
	golang.org/x/sys v0.4.0 // indirect
)
