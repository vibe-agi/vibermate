package runtimepersistence

import (
	"context"
	"database/sql/driver"
	"net/url"
	"strconv"
	"time"

	"modernc.org/sqlite"
)

type sqliteConnector struct {
	driver driver.Driver
	dsn    string
}

var _ driver.Connector = (*sqliteConnector)(nil)

func newSQLiteConnector(databasePath string, busyTimeout time.Duration) driver.Connector {
	return &sqliteConnector{
		driver: &sqlite.Driver{},
		dsn:    sqliteDSN(databasePath, busyTimeout),
	}
}

func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.dsn)
}

func (c *sqliteConnector) Driver() driver.Driver {
	return c.driver
}

func sqliteDSN(databasePath string, busyTimeout time.Duration) string {
	databaseURL := url.URL{
		Scheme: "file",
		Path:   databasePath,
	}
	query := databaseURL.Query()
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Set("_dqs", "false")
	query.Set("_error_rc", "true")
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String()
}
