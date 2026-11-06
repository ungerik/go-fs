module github.com/ungerik/go-fs/cmd/gofs

// An exception to the replace pattern of the backend modules: cmd/gofs
// requires released versions of the go-fs modules without replace
// directives, because "go install github.com/ungerik/go-fs/cmd/gofs@<version>"
// refuses a module whose go.mod has replace directives. Builds in the
// repository use the modules of go.work instead of these versions.
// The release PR sets them to the version it releases with
// ./gofs-require.sh, before that version is tagged.

go 1.26.0

require (
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.15.0
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.8.2
	github.com/aws/aws-sdk-go-v2/config v1.33.13
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.6
	github.com/ungerik/go-fs v1.0.0-rc.7
	github.com/ungerik/go-fs/azureblobfs v1.0.0-rc.7
	github.com/ungerik/go-fs/dropboxfs v1.0.0-rc.7
	github.com/ungerik/go-fs/ftpfs v1.0.0-rc.7
	github.com/ungerik/go-fs/s3fs v1.0.0-rc.7
	github.com/ungerik/go-fs/sftpfs v1.0.0-rc.7
	github.com/ungerik/go-fs/smbfs v1.0.0-rc.7
	github.com/ungerik/go-fs/webdavfs v1.0.0-rc.7
	golang.org/x/crypto v0.58.0
	golang.org/x/oauth2 v0.37.0
)

require (
	github.com/stretchr/testify v1.12.1
	golang.org/x/net v0.62.0
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.23.3 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/storage/internal v0.1.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.10.1 // indirect
	github.com/apache/arrow-go/v18 v18.8.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.47.5 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.25 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.13 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.5 // indirect
	github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager v0.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.8 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.8 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.23 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.12.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.6 // indirect
	github.com/aws/smithy-go v1.28.7 // indirect
	github.com/cloudsoda/go-smb2 v0.0.0-20260918041005-0c5d69b69701 // indirect
	github.com/cloudsoda/sddl v0.0.0-20250224235906-926454e91efc // indirect
	github.com/dropbox/dropbox-sdk-go-unofficial/v6 v6.6.2 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/geoffgarside/ber v1.2.0 // indirect
	github.com/goccy/go-json v0.12.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-uuid v1.0.4 // indirect
	github.com/jcmturner/aescts/v2 v2.0.0 // indirect
	github.com/jcmturner/dnsutils/v2 v2.0.0 // indirect
	github.com/jcmturner/gofork v1.7.6 // indirect
	github.com/jcmturner/goidentity/v6 v6.0.1 // indirect
	github.com/jcmturner/gokrb5/v8 v8.4.4 // indirect
	github.com/jcmturner/rpc/v2 v2.0.3 // indirect
	github.com/jlaffaye/ftp v0.2.4 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pkg/sftp v1.13.12 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
