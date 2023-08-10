module github.com/ungerik/go-fs/dropboxfs

go 1.21

// Parent module in same repo
require github.com/ungerik/go-fs v0.0.0-20230810125331-2caa3ff9c562

replace github.com/ungerik/go-fs => ../

// External
require github.com/tj/go-dropbox v0.0.0-20171107035848-42dd2be3662d

require (
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/segmentio/go-env v1.1.0 // indirect
	github.com/ungerik/go-dry v0.0.0-20230805093253-df9da4cd3437 // indirect
	golang.org/x/sys v0.11.0 // indirect
)
