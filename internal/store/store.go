// Package store persists traffic data to SQLite, PostgreSQL or MySQL/MariaDB.
//
// All counters are written as additive upserts, so late-arriving flow records
// simply add to the bucket they belong to.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// dialect is the SQL flavour of the database in use.
type dialect int

const (
	sqliteDB dialect = iota
	postgresDB
	mysqlDB
)

type DB struct {
	db   *sql.DB
	dial dialect
}

// Open connects to the database and creates the schema if needed.
func Open(driver, dsn string) (*DB, error) {
	var d DB
	var err error
	switch driver {
	case "sqlite", "sqlite3", "":
		if !strings.Contains(dsn, "?") {
			dsn = "file:" + dsn + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)"
		}
		d.db, err = sql.Open("sqlite", dsn)
		if err == nil {
			d.db.SetMaxOpenConns(4)
		}
	case "postgres", "postgresql", "pgx":
		d.dial = postgresDB
		d.db, err = sql.Open("pgx", dsn)
		if err == nil {
			d.db.SetMaxOpenConns(10)
		}
	case "mysql", "mariadb":
		d.dial = mysqlDB
		if dsn, err = mysqlDSN(dsn); err != nil {
			return nil, err
		}
		d.db, err = sql.Open("mysql", dsn)
		if err == nil {
			d.db.SetMaxOpenConns(10)
			d.db.SetConnMaxLifetime(5 * time.Minute) // before the server's wait_timeout closes it
		}
	default:
		return nil, fmt.Errorf("unknown database driver %q (use sqlite, postgres or mysql)", driver)
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect %s: %w", driver, err)
	}
	if err := d.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &d, nil
}

func (d *DB) Close() error { return d.db.Close() }

// mysqlDSN accepts the driver's own format (user:pass@tcp(host:3306)/db) or
// a URL (mysql://user:pass@host:3306/db?tls=true) and returns the former.
func mysqlDSN(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "mysql://") && !strings.HasPrefix(dsn, "mariadb://") {
		if _, err := mysql.ParseDSN(dsn); err != nil {
			return "", fmt.Errorf("mysql connection: %w (example: mysql://user:pass@host:3306/traffic_monitor)", err)
		}
		return dsn, nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("mysql connection URL: %w", err)
	}
	c := mysql.NewConfig()
	c.User = u.User.Username()
	c.Passwd, _ = u.User.Password()
	c.Net = "tcp"
	c.Addr = u.Host
	if u.Port() == "" {
		c.Addr = u.Hostname() + ":3306"
	}
	c.DBName = strings.TrimPrefix(u.Path, "/")
	if c.DBName == "" {
		return "", errors.New("mysql connection URL needs a database name, e.g. mysql://user:pass@host:3306/traffic_monitor")
	}
	for k, v := range u.Query() {
		if k == "tls" {
			c.TLSConfig = v[0]
			continue
		}
		if c.Params == nil {
			c.Params = map[string]string{}
		}
		c.Params[k] = v[0]
	}
	return c.FormatDSN(), nil
}

const schema = `
CREATE TABLE IF NOT EXISTS traffic_minute (
	ts BIGINT NOT NULL, ip TEXT NOT NULL,
	rx BIGINT NOT NULL DEFAULT 0, tx BIGINT NOT NULL DEFAULT 0,
	lan_rx BIGINT NOT NULL DEFAULT 0, lan_tx BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, ip));
CREATE TABLE IF NOT EXISTS traffic_hour (
	ts BIGINT NOT NULL, ip TEXT NOT NULL,
	rx BIGINT NOT NULL DEFAULT 0, tx BIGINT NOT NULL DEFAULT 0,
	lan_rx BIGINT NOT NULL DEFAULT 0, lan_tx BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, ip));
CREATE INDEX IF NOT EXISTS traffic_hour_ip ON traffic_hour (ip, ts);
CREATE INDEX IF NOT EXISTS traffic_minute_ip ON traffic_minute (ip, ts);
CREATE TABLE IF NOT EXISTS peer_hour (
	ts BIGINT NOT NULL, ip TEXT NOT NULL, remote TEXT NOT NULL,
	proto INTEGER NOT NULL, port INTEGER NOT NULL,
	rx BIGINT NOT NULL DEFAULT 0, tx BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, ip, remote, proto, port));
CREATE INDEX IF NOT EXISTS peer_hour_ip ON peer_hour (ip, ts);
CREATE TABLE IF NOT EXISTS iface_minute (
	ts BIGINT NOT NULL, name TEXT NOT NULL,
	in_bytes BIGINT NOT NULL DEFAULT 0, out_bytes BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, name));
CREATE TABLE IF NOT EXISTS iface_hour (
	ts BIGINT NOT NULL, name TEXT NOT NULL,
	in_bytes BIGINT NOT NULL DEFAULT 0, out_bytes BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, name));
CREATE TABLE IF NOT EXISTS hosts (
	ip TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', custom_name TEXT NOT NULL DEFAULT '',
	network TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '',
	first_seen BIGINT NOT NULL DEFAULT 0, last_seen BIGINT NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS ifaces (
	name TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', custom_label TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '', speed BIGINT NOT NULL DEFAULT 0, last_seen BIGINT NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS networks (
	name TEXT PRIMARY KEY, kind TEXT NOT NULL DEFAULT '', cidrs TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS live_hosts (
	ip TEXT PRIMARY KEY, rx_bps DOUBLE PRECISION NOT NULL, tx_bps DOUBLE PRECISION NOT NULL,
	lan_rx_bps DOUBLE PRECISION NOT NULL, lan_tx_bps DOUBLE PRECISION NOT NULL, updated BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS live_ifaces (
	name TEXT PRIMARY KEY, in_bps DOUBLE PRECISION NOT NULL, out_bps DOUBLE PRECISION NOT NULL,
	updated BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// MySQL can't index TEXT columns without a length or create indexes "IF NOT
// EXISTS", and KEY is a reserved word: same tables, MySQL types.
const mysqlSchema = `
CREATE TABLE IF NOT EXISTS traffic_minute (
	ts BIGINT NOT NULL, ip VARCHAR(64) NOT NULL,
	rx BIGINT NOT NULL DEFAULT 0, tx BIGINT NOT NULL DEFAULT 0,
	lan_rx BIGINT NOT NULL DEFAULT 0, lan_tx BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, ip), INDEX traffic_minute_ip (ip, ts));
CREATE TABLE IF NOT EXISTS traffic_hour (
	ts BIGINT NOT NULL, ip VARCHAR(64) NOT NULL,
	rx BIGINT NOT NULL DEFAULT 0, tx BIGINT NOT NULL DEFAULT 0,
	lan_rx BIGINT NOT NULL DEFAULT 0, lan_tx BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, ip), INDEX traffic_hour_ip (ip, ts));
CREATE TABLE IF NOT EXISTS peer_hour (
	ts BIGINT NOT NULL, ip VARCHAR(64) NOT NULL, remote VARCHAR(64) NOT NULL,
	proto INTEGER NOT NULL, port INTEGER NOT NULL,
	rx BIGINT NOT NULL DEFAULT 0, tx BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, ip, remote, proto, port), INDEX peer_hour_ip (ip, ts));
CREATE TABLE IF NOT EXISTS iface_minute (
	ts BIGINT NOT NULL, name VARCHAR(128) NOT NULL,
	in_bytes BIGINT NOT NULL DEFAULT 0, out_bytes BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, name));
CREATE TABLE IF NOT EXISTS iface_hour (
	ts BIGINT NOT NULL, name VARCHAR(128) NOT NULL,
	in_bytes BIGINT NOT NULL DEFAULT 0, out_bytes BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, name));
CREATE TABLE IF NOT EXISTS hosts (
	ip VARCHAR(64) PRIMARY KEY, name VARCHAR(255) NOT NULL DEFAULT '', custom_name VARCHAR(255) NOT NULL DEFAULT '',
	network VARCHAR(255) NOT NULL DEFAULT '', kind VARCHAR(32) NOT NULL DEFAULT '',
	first_seen BIGINT NOT NULL DEFAULT 0, last_seen BIGINT NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS ifaces (
	name VARCHAR(128) PRIMARY KEY, label VARCHAR(255) NOT NULL DEFAULT '', custom_label VARCHAR(255) NOT NULL DEFAULT '',
	kind VARCHAR(32) NOT NULL DEFAULT '', speed BIGINT NOT NULL DEFAULT 0, last_seen BIGINT NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS networks (
	name VARCHAR(255) PRIMARY KEY, kind VARCHAR(32) NOT NULL DEFAULT '', cidrs TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS live_hosts (
	ip VARCHAR(64) PRIMARY KEY, rx_bps DOUBLE PRECISION NOT NULL, tx_bps DOUBLE PRECISION NOT NULL,
	lan_rx_bps DOUBLE PRECISION NOT NULL, lan_tx_bps DOUBLE PRECISION NOT NULL, updated BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS live_ifaces (
	name VARCHAR(128) PRIMARY KEY, in_bps DOUBLE PRECISION NOT NULL, out_bps DOUBLE PRECISION NOT NULL,
	updated BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS meta (` + "`key`" + ` VARCHAR(128) PRIMARY KEY, value TEXT NOT NULL);
`

func (d *DB) migrate(ctx context.Context) error {
	ddl := schema
	if d.dial == mysqlDB {
		ddl = mysqlSchema
	}
	for _, stmt := range strings.Split(ddl, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w\n%s", err, stmt)
		}
	}
	return nil
}

// q rewrites ? placeholders to $N for PostgreSQL.
func (d *DB) q(s string) string {
	if d.dial != postgresDB {
		return s
	}
	var b strings.Builder
	n := 0
	for _, c := range s {
		if c == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- writes

type HostBucket struct {
	TS                   int64 // unix seconds, start of minute
	IP                   string
	Rx, Tx, LanRx, LanTx uint64
}

type PeerBucket struct {
	TS         int64 // unix seconds, start of hour
	IP, Remote string
	Proto      uint8
	Port       uint16
	Rx, Tx     uint64
}

type IfaceBucket struct {
	TS      int64 // unix seconds, start of minute
	Name    string
	In, Out uint64
}

type HostInfo struct {
	IP, Name, Network, Kind string
	Seen                    int64
}

type IfaceInfo struct {
	Name, Label, Kind string
	Speed             uint64
}

type LiveHost struct {
	IP                               string
	RxBps, TxBps, LanRxBps, LanTxBps float64
}

type LiveIface struct {
	Name          string
	InBps, OutBps float64
}

// upsert starts the "insert or update" clause for a row that already exists
// (by primary key); ex names the value the INSERT tried to write.
func (d *DB) upsert(key string) string {
	if d.dial == mysqlDB {
		return " ON DUPLICATE KEY UPDATE "
	}
	return " ON CONFLICT (" + key + ") DO UPDATE SET "
}

func (d *DB) ex(col string) string {
	if d.dial == mysqlDB {
		return "VALUES(" + col + ")"
	}
	return "excluded." + col
}

// sum totals a column as a 64-bit integer.
func (d *DB) sum(col string) string {
	if d.dial == mysqlDB {
		return "CAST(SUM(" + col + ") AS SIGNED)"
	}
	return "CAST(SUM(" + col + ") AS BIGINT)"
}

// metaKey is the meta table's key column (KEY is reserved in MySQL).
func (d *DB) metaKey() string {
	if d.dial == mysqlDB {
		return "`key`"
	}
	return "key"
}

func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func i64(v uint64) int64 { return int64(v) }

func (d *DB) WriteTraffic(ctx context.Context, hosts []HostBucket, peers []PeerBucket) error {
	if len(hosts) == 0 && len(peers) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		up := func(table string) (*sql.Stmt, error) {
			return tx.PrepareContext(ctx, d.q(`INSERT INTO `+table+` (ts, ip, rx, tx, lan_rx, lan_tx) VALUES (?,?,?,?,?,?)`+
				d.upsert("ts, ip")+`rx = `+table+`.rx + `+d.ex("rx")+`, tx = `+table+`.tx + `+d.ex("tx")+`,
				lan_rx = `+table+`.lan_rx + `+d.ex("lan_rx")+`, lan_tx = `+table+`.lan_tx + `+d.ex("lan_tx")))
		}
		minute, err := up("traffic_minute")
		if err != nil {
			return err
		}
		defer minute.Close()
		hour, err := up("traffic_hour")
		if err != nil {
			return err
		}
		defer hour.Close()
		for _, h := range hosts {
			args := []any{h.TS, h.IP, i64(h.Rx), i64(h.Tx), i64(h.LanRx), i64(h.LanTx)}
			if _, err := minute.ExecContext(ctx, args...); err != nil {
				return err
			}
			args[0] = h.TS - h.TS%3600
			if _, err := hour.ExecContext(ctx, args...); err != nil {
				return err
			}
		}
		if len(peers) == 0 {
			return nil
		}
		peer, err := tx.PrepareContext(ctx, d.q(`INSERT INTO peer_hour (ts, ip, remote, proto, port, rx, tx) VALUES (?,?,?,?,?,?,?)`+
			d.upsert("ts, ip, remote, proto, port")+`rx = peer_hour.rx + `+d.ex("rx")+`, tx = peer_hour.tx + `+d.ex("tx")))
		if err != nil {
			return err
		}
		defer peer.Close()
		for _, p := range peers {
			if _, err := peer.ExecContext(ctx, p.TS, p.IP, p.Remote, int(p.Proto), int(p.Port), i64(p.Rx), i64(p.Tx)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) WriteIfaces(ctx context.Context, buckets []IfaceBucket) error {
	if len(buckets) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"iface_minute", "iface_hour"} {
			st, err := tx.PrepareContext(ctx, d.q(`INSERT INTO `+table+` (ts, name, in_bytes, out_bytes) VALUES (?,?,?,?)`+
				d.upsert("ts, name")+`in_bytes = `+table+`.in_bytes + `+d.ex("in_bytes")+`,
				out_bytes = `+table+`.out_bytes + `+d.ex("out_bytes")))
			if err != nil {
				return err
			}
			for _, b := range buckets {
				ts := b.TS
				if table == "iface_hour" {
					ts -= ts % 3600
				}
				if _, err := st.ExecContext(ctx, ts, b.Name, i64(b.In), i64(b.Out)); err != nil {
					st.Close()
					return err
				}
			}
			st.Close()
		}
		return nil
	})
}

func (d *DB) UpsertHosts(ctx context.Context, hosts []HostInfo) error {
	if len(hosts) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		name, seen := d.ex("name"), d.ex("last_seen")
		st, err := tx.PrepareContext(ctx, d.q(`INSERT INTO hosts (ip, name, network, kind, first_seen, last_seen) VALUES (?,?,?,?,?,?)`+
			d.upsert("ip")+`
				name = CASE WHEN `+name+` <> '' THEN `+name+` ELSE hosts.name END,
				network = `+d.ex("network")+`, kind = `+d.ex("kind")+`,
				last_seen = CASE WHEN `+seen+` > hosts.last_seen THEN `+seen+` ELSE hosts.last_seen END`))
		if err != nil {
			return err
		}
		defer st.Close()
		for _, h := range hosts {
			if _, err := st.ExecContext(ctx, h.IP, h.Name, h.Network, h.Kind, h.Seen, h.Seen); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) UpsertIfaces(ctx context.Context, ifs []IfaceInfo) error {
	now := time.Now().Unix()
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, i := range ifs {
			_, err := tx.ExecContext(ctx, d.q(`INSERT INTO ifaces (name, label, kind, speed, last_seen) VALUES (?,?,?,?,?)`+
				d.upsert("name")+`label = `+d.ex("label")+`, kind = `+d.ex("kind")+`,
				speed = `+d.ex("speed")+`, last_seen = `+d.ex("last_seen")), i.Name, i.Label, i.Kind, i64(i.Speed), now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) SyncNetworks(ctx context.Context, nets []Network) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM networks`); err != nil {
			return err
		}
		for _, n := range nets {
			if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO networks (name, kind, cidrs) VALUES (?,?,?)`),
				n.Name, n.Kind, n.CIDRs); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) SetLiveHosts(ctx context.Context, hosts []LiveHost) error {
	now := time.Now().Unix()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM live_hosts`); err != nil {
			return err
		}
		st, err := tx.PrepareContext(ctx, d.q(`INSERT INTO live_hosts (ip, rx_bps, tx_bps, lan_rx_bps, lan_tx_bps, updated) VALUES (?,?,?,?,?,?)`))
		if err != nil {
			return err
		}
		defer st.Close()
		for _, h := range hosts {
			if _, err := st.ExecContext(ctx, h.IP, h.RxBps, h.TxBps, h.LanRxBps, h.LanTxBps, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) SetLiveIfaces(ctx context.Context, ifs []LiveIface) error {
	now := time.Now().Unix()
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM live_ifaces`); err != nil {
			return err
		}
		for _, i := range ifs {
			if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO live_ifaces (name, in_bps, out_bps, updated) VALUES (?,?,?,?)`),
				i.Name, i.InBps, i.OutBps, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) SetMeta(ctx context.Context, kv map[string]string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for k, v := range kv {
			if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO meta (`+d.metaKey()+`, value) VALUES (?,?)`+
				d.upsert("key")+`value = `+d.ex("value")), k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// Prune deletes data older than the retention windows (days <= 0 keeps forever).
func (d *DB) Prune(ctx context.Context, minuteDays, hourDays, peerDays int) error {
	now := time.Now()
	del := func(table string, days int) error {
		if days <= 0 {
			return nil
		}
		cut := now.AddDate(0, 0, -days).Unix()
		_, err := d.db.ExecContext(ctx, d.q(`DELETE FROM `+table+` WHERE ts < ?`), cut)
		return err
	}
	for _, x := range []struct {
		t string
		d int
	}{{"traffic_minute", minuteDays}, {"iface_minute", minuteDays}, {"traffic_hour", hourDays}, {"iface_hour", hourDays}, {"peer_hour", peerDays}} {
		if err := del(x.t, x.d); err != nil {
			return fmt.Errorf("prune %s: %w", x.t, err)
		}
	}
	return nil
}
