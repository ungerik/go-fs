module github.com/ungerik/go-fs/webdavfs

go 1.26.0

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

require (
	github.com/stretchr/testify v1.12.1
	golang.org/x/net v0.58.0
)

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
