package recoverer

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"mysql-pitr-helper/pxc"
	"mysql-pitr-helper/storage"

	"github.com/pkg/errors"
)

type Recoverer struct {
	db             *pxc.PXC
	recoverTime    string
	storage        storage.Storage
	host           string
	user           string
	pass           string
	recoverType    RecoverType
	binlogs        []string
	gtidSet        string
	startGTID      string
	recoverFlag    string
	recoverEndTime time.Time
	gtid           string
	verifyTLS      bool
}

type Config struct {
	Host               string `env:"HOST,required"`
	User               string `env:"USER,required"`
	Pass               string `env:"PASS,required"`
	RecoverTime        string `env:"PITR_DATE"`
	RecoverType        string `env:"PITR_RECOVERY_TYPE,required"`
	GTID               string `env:"PITR_GTID"`
	VerifyTLS          bool   `env:"VERIFY_TLS" envDefault:"true"`
	StorageType        string `env:"STORAGE_TYPE,required"`
	BinlogStorageS3    BinlogS3
	BinlogStorageAzure BinlogAzure
}

func (c Config) storage(ctx context.Context) (storage.Storage, error) {
	var binlogStorage storage.Storage
	switch c.StorageType {
	case "s3":
		bucket, prefix, err := getBucketAndPrefix(c.BinlogStorageS3.BucketURL)
		if err != nil {
			return nil, errors.Wrap(err, "get bucket and prefix")
		}
		binlogStorage, err = storage.NewS3(ctx, c.BinlogStorageS3.Endpoint, c.BinlogStorageS3.AccessKeyID, c.BinlogStorageS3.AccessKey, bucket, prefix, c.BinlogStorageS3.Region, c.VerifyTLS)
		if err != nil {
			return nil, errors.Wrap(err, "new s3 storage")
		}
	case "azure":
		var err error
		container, prefix := getContainerAndPrefix(c.BinlogStorageAzure.ContainerPath)
		binlogStorage, err = storage.NewAzure(c.BinlogStorageAzure.AccountName, c.BinlogStorageAzure.AccountKey, c.BinlogStorageAzure.Endpoint, container, prefix)
		if err != nil {
			return nil, errors.Wrap(err, "new azure storage")
		}
	default:
		return nil, errors.New("unknown STORAGE_TYPE")
	}
	return binlogStorage, nil
}

type BinlogS3 struct {
	Endpoint    string `env:"BINLOG_S3_ENDPOINT" envDefault:"s3.amazonaws.com"`
	AccessKeyID string `env:"BINLOG_ACCESS_KEY_ID,required"`
	AccessKey   string `env:"BINLOG_SECRET_ACCESS_KEY,required"`
	Region      string `env:"BINLOG_S3_REGION,required"`
	BucketURL   string `env:"BINLOG_S3_BUCKET_URL,required"`
}

type BinlogAzure struct {
	Endpoint      string `env:"BINLOG_AZURE_ENDPOINT,required"`
	ContainerPath string `env:"BINLOG_AZURE_CONTAINER_PATH,required"`
	StorageClass  string `env:"BINLOG_AZURE_STORAGE_CLASS"`
	AccountName   string `env:"BINLOG_AZURE_STORAGE_ACCOUNT,required"`
	AccountKey    string `env:"BINLOG_AZURE_ACCESS_KEY,required"`
}

func (c *Config) Verify() {
	if len(c.BinlogStorageS3.Endpoint) == 0 {
		c.BinlogStorageS3.Endpoint = "s3.amazonaws.com"
	}
}

type RecoverType string

func New(ctx context.Context, c Config) (*Recoverer, error) {
	c.Verify()

	binlogStorage, err := c.storage(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "new binlog storage manager")
	}

	return &Recoverer{
		storage:     binlogStorage,
		recoverTime: c.RecoverTime,
		host:        c.Host,
		user:        c.User,
		pass:        c.Pass,
		recoverType: RecoverType(c.RecoverType),
		gtid:        c.GTID,
		verifyTLS:   c.VerifyTLS,
	}, nil
}

func getContainerAndPrefix(s string) (string, string) {
	container, prefix, _ := strings.Cut(s, "/")
	if prefix != "" {
		prefix += "/"
	}
	return container, prefix
}

func getBucketAndPrefix(bucketURL string) (bucket string, prefix string, err error) {
	u, err := url.Parse(bucketURL)
	if err != nil {
		err = errors.Wrap(err, "parse url")
		return bucket, prefix, err
	}
	path := strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/")

	if u.IsAbs() && u.Scheme == "s3" {
		bucket = u.Host
		prefix = path + "/"
		return bucket, prefix, err
	}
	bucketArr := strings.Split(path, "/")
	if len(bucketArr) > 1 {
		prefix = strings.TrimPrefix(path, bucketArr[0]+"/") + "/"
	}
	bucket = bucketArr[0]
	if len(bucket) == 0 {
		err = errors.Errorf("can't get bucket name from %s", bucketURL)
		return bucket, prefix, err
	}

	return bucket, prefix, err
}

const (
	Latest      RecoverType = "latest"      // recover to the latest existing binlog
	Date        RecoverType = "date"        // recover to exact date
	Transaction RecoverType = "transaction" // recover to needed trunsaction
	Skip        RecoverType = "skip"        // skip transactions
)

func (r *Recoverer) Run(ctx context.Context) error {
	var err error
	r.db, err = pxc.NewPXC(r.host, r.user, r.pass)
	if err != nil {
		return errors.Wrapf(err, "new manager with host %s", r.host)
	}

	r.startGTID, err = r.db.GetCurrentGTIDSet(ctx)
	if err != nil {
		return errors.Wrap(err, "get start GTID")
	}

	if r.recoverType == Transaction {
		err = r.verifyTransactionInputGTID(ctx)
		if err != nil {
			return errors.Wrap(err, "verify transaction num to restore")
		}
	}

	err = r.setBinlogs(ctx)
	if err != nil {
		return errors.Wrap(err, "get binlog list")
	}

	switch r.recoverType {
	case Skip:
		r.recoverFlag = `--exclude-gtids="` + r.gtid + `"`
	case Transaction:
		r.recoverFlag = `--exclude-gtids="` + r.gtidSet + `"`
	case Date:
		r.recoverFlag = `--stop-datetime="` + r.recoverTime + `"`

		const format = "2006-01-02 15:04:05"
		endTime, err := time.Parse(format, r.recoverTime)
		if err != nil {
			return errors.Wrap(err, "parse date")
		}
		r.recoverEndTime = endTime
	case Latest:
	default:
		return errors.New("wrong recover type")
	}

	err = r.recover(ctx)
	if err != nil {
		return errors.Wrap(err, "recover")
	}

	return nil
}

func (r *Recoverer) recover(ctx context.Context) (err error) {
	err = r.db.DropCollectorFunctions(ctx)
	if err != nil {
		return errors.Wrap(err, "drop collector funcs")
	}

	err = os.Setenv("MYSQL_PWD", r.pass)
	if err != nil {
		return errors.Wrap(err, "set mysql pwd env var")
	}

	mysqlStdin, binlogStdout := io.Pipe()
	defer mysqlStdin.Close()

	mysqlCmd := exec.CommandContext(ctx, "mysql", "-h", r.db.GetHost(), "-P", "33062", "-u", r.user)
	log.Printf("Running %s", mysqlCmd.String())
	mysqlCmd.Stdin = mysqlStdin
	mysqlCmd.Stderr = os.Stderr
	mysqlCmd.Stdout = os.Stdout
	if err := mysqlCmd.Start(); err != nil {
		return errors.Wrap(err, "start mysql")
	}

	for i, binlog := range r.binlogs {
		remaining := len(r.binlogs) - i
		log.Printf("working with %s, %d out of %d remaining\n", binlog, remaining, len(r.binlogs))
		if r.recoverType == Date {
			binlogArr := strings.Split(binlog, "_")
			if len(binlogArr) < 2 {
				return errors.New("get timestamp from binlog name")
			}
			binlogTime, err := strconv.ParseInt(binlogArr[1], 10, 64)
			if err != nil {
				return errors.Wrap(err, "get binlog time")
			}
			if binlogTime > r.recoverEndTime.Unix() {
				log.Printf("Stopping at %s because it's after the recovery time (%d > %d)", binlog, binlogTime, r.recoverEndTime.Unix())
				break
			}
		}

		binlogObj, err := r.storage.GetObject(ctx, binlog)
		if err != nil {
			return errors.Wrap(err, "get obj")
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", "mysqlbinlog --disable-log-bin "+r.recoverFlag+" -")
		log.Printf("Running %s", cmd.String())
		cmd.Stdin = binlogObj
		cmd.Stdout = binlogStdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
		if err != nil {
			return errors.Wrapf(err, "run mysqlbinlog")
		}
	}

	if err := binlogStdout.Close(); err != nil {
		return errors.Wrap(err, "close binlog stdout")
	}

	log.Printf("Waiting for mysql to finish")

	if err := mysqlCmd.Wait(); err != nil {
		return errors.Wrap(err, "wait mysql")
	}

	log.Printf("Finished")

	return nil
}

func (r *Recoverer) setBinlogs(ctx context.Context) error {
	list, err := r.storage.ListObjects(ctx, "binlog_")
	if err != nil {
		return errors.Wrap(err, "list objects with prefix 'binlog_'")
	}
	reverse(list)
	binlogs := []string{}
	log.Println("current gtid set is", r.startGTID)
	for _, binlog := range list {
		if strings.Contains(binlog, "-gtid-set") {
			continue
		}
		infoObj, err := r.storage.GetObject(ctx, binlog+"-gtid-set")
		if err != nil {
			log.Println("Can't get binlog object with gtid set. Name:", binlog, "error", err)
			continue
		}
		content, err := io.ReadAll(infoObj)
		if err != nil {
			return errors.Wrapf(err, "read %s gtid-set object", binlog)
		}
		binlogGTIDSet := string(content)
		log.Println("checking current file", " name ", binlog, " gtid ", binlogGTIDSet)

		if len(r.gtid) > 0 && r.recoverType == Transaction {
			subResult, err := r.db.SubtractGTIDSet(ctx, binlogGTIDSet, r.gtid)
			if err != nil {
				return errors.Wrapf(err, "check if '%s' is a subset of '%s", binlogGTIDSet, r.gtid)
			}
			if subResult != binlogGTIDSet {
				set, err := r.getExtendGTIDSet(ctx, binlogGTIDSet, r.gtid)
				if err != nil {
					return errors.Wrap(err, "get gtid set for extend")
				}
				r.gtidSet = set
			}
			if len(r.gtidSet) == 0 {
				continue
			}
		}

		binlogs = append(binlogs, binlog)
		subResult, err := r.db.SubtractGTIDSet(ctx, r.startGTID, binlogGTIDSet)
		log.Println("Checking sub result", " binlog gtid ", binlogGTIDSet, " sub result ", subResult)
		if err != nil {
			return errors.Wrapf(err, "check if '%s' is a subset of '%s", r.startGTID, binlogGTIDSet)
		}
		if subResult != r.startGTID {
			break
		}
	}
	if len(binlogs) == 0 {
		return errors.Errorf("no objects for prefix binlog_ or with gtid=%s", r.gtid)
	}
	reverse(binlogs)
	r.binlogs = binlogs

	return nil
}

func (r *Recoverer) verifyTransactionInputGTID(ctx context.Context) error {
	gtidSplit := strings.Split(r.gtid, ":")
	if len(gtidSplit) != 2 || strings.Contains(gtidSplit[1], "-") {
		return errors.New("bad transaction num format")
	}
	subResult, err := r.db.SubtractGTIDSet(ctx, r.startGTID, r.gtid)
	if err != nil {
		return errors.Wrap(err, "transaction num is malformed or gtid subtract query exception occurred")
	}
	if subResult != r.startGTID {
		return errors.New("can't restore to the transaction before backup")
	}
	return nil
}

func (r *Recoverer) getExtendGTIDSet(ctx context.Context, gtidSet, gtid string) (string, error) {
	if gtidSet == gtid {
		return gtid, nil
	}

	if len(strings.Split(gtidSet, ",")) != 1 {
		return "", errors.New("binlog contains multiple gtid records, can't exactly determine which to exclude")
	}

	gtidSplit := strings.Split(gtid, ":")
	gtidNum, err := strconv.ParseInt(gtidSplit[1], 10, 64)
	if err != nil {
		return "", errors.Wrap(err, "parse gtid transaction num")
	}

	excludeSet, err := r.db.SubtractGTIDSet(ctx, gtidSet, fmt.Sprintf("%s:1-%v", gtidSplit[0], gtidNum-1))
	if err != nil {
		return "", errors.Wrap(err, "failed to subtract gtid set")
	}

	return excludeSet, nil
}

func reverse(list []string) {
	for i := len(list)/2 - 1; i >= 0; i-- {
		opp := len(list) - 1 - i
		list[i], list[opp] = list[opp], list[i]
	}
}
