package storage

type BackupStorageType string

const (
	BackupStorageFilesystem BackupStorageType = "filesystem"
	BackupStorageS3         BackupStorageType = "s3"
	BackupStorageAzure      BackupStorageType = "azure"
)

type Options interface {
	Type() BackupStorageType
}

var _ = Options(new(S3Options))

type S3Options struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	Prefix          string
	Region          string
	VerifyTLS       bool
}

func (o *S3Options) Type() BackupStorageType {
	return BackupStorageS3
}

var _ = Options(new(AzureOptions))

type AzureOptions struct {
	StorageAccount string
	AccessKey      string
	Endpoint       string
	Container      string
	Prefix         string
}

func (o *AzureOptions) Type() BackupStorageType {
	return BackupStorageAzure
}
