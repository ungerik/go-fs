module github.com/ungerik/go-fs/dropboxfs

go 1.19

replace github.com/ungerik/go-fs => ../

require (
	github.com/tj/go-dropbox v0.0.0-20171107035848-42dd2be3662d
	github.com/ungerik/go-fs v0.0.0-20220917143557-36801f64eb35
)

require (
	github.com/fsnotify/fsnotify v1.5.4 // indirect
	golang.org/x/sys v0.0.0-20220915200043-7b5979e65e41 // indirect
)
