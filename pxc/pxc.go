package pxc

import (
	"context"
	"database/sql"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
)

const UsingPassErrorMessage = `mysqlbinlog: [Warning] Using a password on the command line interface can be insecure.`

// PXC is a type for working with pxc
type PXC struct {
	db   *sql.DB // handle for work with database
	host string  // host for connection
}

// NewManager return new manager for work with pxc
func NewPXC(addr string, user, pass string) (*PXC, error) {
	var pxc PXC

	config := mysql.NewConfig()
	config.User = user
	config.Passwd = pass
	config.Net = "tcp"
	config.Addr = addr + ":33062"
	config.Params = map[string]string{"interpolateParams": "true"}

	mysqlDB, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, errors.Wrap(err, "cannot connect to host")
	}

	pxc.db = mysqlDB
	pxc.host = addr

	return &pxc, nil
}

// Close is for closing db connection
func (p *PXC) Close() error {
	return p.db.Close()
}

// GetHost returns pxc host
func (p *PXC) GetHost() string {
	return p.host
}

// GetGTIDSet return GTID set by binary log file name
func (p *PXC) GetGTIDSet(ctx context.Context, binlogName string) (string, error) {
	// select name from mysql.func where name='get_gtid_set_by_binlog'
	var existFunc string
	nameRow := p.db.QueryRowContext(ctx, "select name from mysql.func where name='get_gtid_set_by_binlog'")
	err := nameRow.Scan(&existFunc)
	if err != nil && err != sql.ErrNoRows {
		return "", errors.Wrap(err, "get udf name")
	}
	if len(existFunc) == 0 {
		_, err = p.db.ExecContext(ctx, "CREATE FUNCTION get_gtid_set_by_binlog RETURNS STRING SONAME 'binlog_utils_udf.so'")
		if err != nil {
			return "", errors.Wrap(err, "create function")
		}
	}
	var binlogSet string
	row := p.db.QueryRowContext(ctx, "SELECT get_gtid_set_by_binlog(?)", binlogName)
	err = row.Scan(&binlogSet)
	if err != nil && !strings.Contains(err.Error(), "Binary log does not exist") {
		return "", errors.Wrap(err, "scan set")
	}

	return binlogSet, nil
}

type Binlog struct {
	Name      string
	Size      int64
	Encrypted string
	GTIDSet   GTIDSet
}

type GTIDSet struct {
	gtidSet string
}

func NewGTIDSet(gtidSet string) GTIDSet {
	return GTIDSet{gtidSet: gtidSet}
}

func (s *GTIDSet) IsEmpty() bool {
	return len(s.gtidSet) == 0
}

func (s *GTIDSet) Raw() string {
	return s.gtidSet
}

func (s *GTIDSet) List() []string {
	if len(s.gtidSet) == 0 {
		return nil
	}
	list := strings.Split(s.gtidSet, ",")
	sort.Strings(list)
	return list
}

// GetBinLogList return binary log files list
func (p *PXC) GetBinLogList(ctx context.Context) ([]Binlog, error) {
	rows, err := p.db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return nil, errors.Wrap(err, "show binary logs")
	}

	var binlogs []Binlog
	for rows.Next() {
		var b Binlog
		if err := rows.Scan(&b.Name, &b.Size, &b.Encrypted); err != nil {
			return nil, errors.Wrap(err, "scan binlogs")
		}
		binlogs = append(binlogs, b)
	}

	_, err = p.db.ExecContext(ctx, "FLUSH BINARY LOGS")
	if err != nil {
		return nil, errors.Wrap(err, "flush binary logs")
	}

	return binlogs, nil
}

// GetBinLogList return binary log files list
func (p *PXC) GetBinLogNamesList(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return nil, errors.Wrap(err, "show binary logs")
	}
	defer rows.Close()

	var binlogs []string
	for rows.Next() {
		var b Binlog
		if err := rows.Scan(&b.Name, &b.Size, &b.Encrypted); err != nil {
			return nil, errors.Wrap(err, "scan binlogs")
		}
		binlogs = append(binlogs, b.Name)
	}

	return binlogs, nil
}

func (p *PXC) GTIDSubset(ctx context.Context, set1, set2 string) (bool, error) {
	row := p.db.QueryRowContext(ctx, "SELECT GTID_SUBSET(?,?)", set1, set2)
	var result int
	if err := row.Scan(&result); err != nil {
		return false, errors.Wrap(err, "scan result")
	}

	return result == 1, nil
}

// GetBinLogFirstTimestamp return binary log file first timestamp
func (p *PXC) GetBinLogFirstTimestamp(ctx context.Context, binlog string) (string, error) {
	var existFunc string
	nameRow := p.db.QueryRowContext(ctx, "select name from mysql.func where name='get_first_record_timestamp_by_binlog'")
	err := nameRow.Scan(&existFunc)
	if err != nil && err != sql.ErrNoRows {
		return "", errors.Wrap(err, "get udf name")
	}
	if len(existFunc) == 0 {
		_, err = p.db.ExecContext(ctx, "CREATE FUNCTION get_first_record_timestamp_by_binlog RETURNS INTEGER SONAME 'binlog_utils_udf.so'")
		if err != nil {
			return "", errors.Wrap(err, "create function")
		}
	}
	var timestamp string
	row := p.db.QueryRowContext(ctx, "SELECT get_first_record_timestamp_by_binlog(?) DIV 1000000", binlog)

	err = row.Scan(&timestamp)
	if err != nil {
		return "", errors.Wrap(err, "scan binlog timestamp")
	}

	return timestamp, nil
}

// GetBinLogLastTimestamp return binary log file last timestamp
func (p *PXC) GetBinLogLastTimestamp(ctx context.Context, binlog string) (string, error) {
	var existFunc string
	nameRow := p.db.QueryRowContext(ctx, "select name from mysql.func where name='get_last_record_timestamp_by_binlog'")
	err := nameRow.Scan(&existFunc)
	if err != nil && err != sql.ErrNoRows {
		return "", errors.Wrap(err, "get udf name")
	}
	if len(existFunc) == 0 {
		_, err = p.db.ExecContext(ctx, "CREATE FUNCTION get_last_record_timestamp_by_binlog RETURNS INTEGER SONAME 'binlog_utils_udf.so'")
		if err != nil {
			return "", errors.Wrap(err, "create function")
		}
	}
	var timestamp string
	row := p.db.QueryRowContext(ctx, "SELECT get_last_record_timestamp_by_binlog(?) DIV 1000000", binlog)

	err = row.Scan(&timestamp)
	if err != nil {
		return "", errors.Wrap(err, "scan binlog timestamp")
	}

	return timestamp, nil
}

func (p *PXC) GetCurrentGTIDSet(ctx context.Context) (string, error) {
	var result string
	row := p.db.QueryRowContext(ctx, "SELECT @@GLOBAL.gtid_executed;")
	err := row.Scan(&result)
	if err != nil {
		return "", errors.Wrap(err, "scan current gtid_executed result")
	}

	return result, nil
}

func (p *PXC) SubtractGTIDSet(ctx context.Context, set, subSet string) (string, error) {
	var result string
	row := p.db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?,?)", set, subSet)
	err := row.Scan(&result)
	if err != nil {
		return "", errors.Wrap(err, "scan gtid subtract result")
	}

	return result, nil
}

func (p *PXC) GetHealthyClusterMembers(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT MEMBER_HOST FROM performance_schema.replication_group_members WHERE MEMBER_STATE = 'ONLINE'")
	if err != nil {
		return nil, errors.Wrap(err, "select replication_group_members")
	}
	defer rows.Close()

	var hosts []string
	for rows.Next() {
		var host string
		if err = rows.Scan(&host); err != nil {
			return nil, errors.Wrap(err, "scan host")
		}
		hosts = append(hosts, host)
	}

	return hosts, nil
}

func FilterHealthyClusterMembers(ctx context.Context, hosts []string, user, pass string) ([]string, error) {
	var healthyMembers []string
	for _, host := range hosts {
		db, err := NewPXC(host, user, pass)
		if err != nil {
			log.Printf("ERROR: creating connection for host %s: %v", host, err)
			continue
		}
		healthyMembers, err = db.GetHealthyClusterMembers(ctx)
		db.Close()
		if err != nil {
			log.Printf("ERROR: get healthy cluster members for host %s: %v", host, err)
			continue
		}
		if len(healthyMembers) != 0 {
			break
		}
	}
	if len(healthyMembers) == 0 {
		return nil, errors.New("no healthy cluster members detected")
	}
	var healthyHosts []string
	for _, host := range hosts {
		if slices.Contains(healthyMembers, host) {
			healthyHosts = append(healthyHosts, host)
		}
	}
	if len(healthyHosts) == 0 {
		return nil, errors.New("no healthy cluster members found in provided hosts")
	}
	return healthyHosts, nil
}

func GetPXCOldestBinlogHost(ctx context.Context, hosts []string, user, pass string) (string, error) {
	var oldestHost string
	var oldestTS int64
	for _, host := range hosts {
		binlogTime, err := getBinlogTime(ctx, host, user, pass)
		if err != nil {
			log.Printf("ERROR: get binlog time %v", err)
			continue
		}
		if len(oldestHost) == 0 || oldestTS > 0 && binlogTime < oldestTS {
			oldestHost = host
			oldestTS = binlogTime
		}
	}

	if len(oldestHost) == 0 {
		return "", errors.New("can't find host")
	}

	return oldestHost, nil
}

func getBinlogTime(ctx context.Context, host, user, pass string) (int64, error) {
	db, err := NewPXC(host, user, pass)
	if err != nil {
		return 0, errors.Errorf("creating connection for host %s: %v", host, err)
	}
	defer db.Close()
	list, err := db.GetBinLogNamesList(ctx)
	if err != nil {
		return 0, errors.Errorf("get binlog list for host %s: %v", host, err)
	}
	if len(list) == 0 {
		return 0, errors.Errorf("get binlog list for host %s: no binlogs found", host)
	}
	var binlogTime int64
	for _, binlogName := range list {
		binlogTime, err = getBinlogTimeByName(ctx, db, binlogName)
		if err != nil {
			log.Printf("ERROR: get binlog timestamp for binlog %s host %s: %v", binlogName, host, err)
			continue
		}
		if binlogTime > 0 {
			break
		}
	}
	if binlogTime == 0 {
		return 0, errors.Errorf("get binlog oldest timestamp for host %s: no binlogs timestamp found", host)
	}

	return binlogTime, nil
}

func getBinlogTimeByName(ctx context.Context, db *PXC, binlogName string) (int64, error) {
	ts, err := db.GetBinLogFirstTimestamp(ctx, binlogName)
	if err != nil {
		return 0, errors.Wrap(err, "get binlog first timestamp")
	}
	binlogTime, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0, errors.Wrap(err, "parse timestamp")
	}

	return binlogTime, nil
}

func (p *PXC) DropCollectorFunctions(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, "DROP FUNCTION IF EXISTS get_first_record_timestamp_by_binlog")
	if err != nil {
		return errors.Wrap(err, "drop get_first_record_timestamp_by_binlog function")
	}
	_, err = p.db.ExecContext(ctx, "DROP FUNCTION IF EXISTS get_binlog_by_gtid_set")
	if err != nil {
		return errors.Wrap(err, "drop get_binlog_by_gtid_set function")
	}

	_, err = p.db.ExecContext(ctx, "DROP FUNCTION IF EXISTS get_gtid_set_by_binlog")
	if err != nil {
		return errors.Wrap(err, "drop get_gtid_set_by_binlog function")
	}

	return nil
}
