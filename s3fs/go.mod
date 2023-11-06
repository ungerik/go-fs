module github.com/ungerik/go-fs/s3fs

go 1.21

// Parent module in same repo
require github.com/ungerik/go-fs v0.0.0-20230828210517-6ca798932ba7

replace github.com/ungerik/go-fs => ../

// External
require github.com/aws/aws-sdk-go-v2/service/s3 v1.42.0

require (
	github.com/aws/aws-sdk-go-v2 v1.22.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.22.0
	github.com/aws/aws-sdk-go-v2/credentials v1.15.1 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.14.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.2.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.2.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.10.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.2.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.10.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.16.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.17.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.19.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.25.0 // indirect
	github.com/aws/smithy-go v1.16.0 // indirect
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	golang.org/x/sys v0.14.0 // indirect
)
