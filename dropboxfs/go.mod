module github.com/ungerik/go-fs/dropboxfs

go 1.24.0

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

// External
require github.com/tj/go-dropbox v0.0.0-20171107035848-42dd2be3662d

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/segmentio/go-env v1.1.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/ungerik/go-dry v0.0.0-20231011182423-d9a07fd18c5f // indirect
	golang.org/x/sys v0.37.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
