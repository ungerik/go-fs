module github.com/ungerik/go-fs/ftpfs

go 1.21.0

replace github.com/ungerik/go-fs => ../

require (
	github.com/jlaffaye/ftp v0.2.0
	github.com/ungerik/go-fs v0.0.0-20231106161718-8730eb08d309
)

require (
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	golang.org/x/sys v0.14.0 // indirect
)
