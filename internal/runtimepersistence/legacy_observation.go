package runtimepersistence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrInspectionUnavailable = recording.ErrInspectionUnavailable
	ErrInspectionSourceLimit = errors.New("inspection resources exceed supported limits")
	ErrInspectionChanged     = recording.ErrInspectionChanged
	ErrInspectionCursor      = recording.ErrInspectionCursor
)

// Sources are declared layout metadata, never discovered by scanning history.
// Current is last; archive publication order is traversal order, not event time.
type InspectionSource struct {
	ID, RelativePath string
	Database         SchemaState
	Current          bool
}
type InspectionCatalogOptions struct {
	Root, LayoutID string
	Sources        []InspectionSource
	Generation     uint64
	Concurrency    int
	Timeout        time.Duration
}
type InspectionVersion = recording.InspectionSourceVersion

// The catalog shares only lazy read ownership. Domain adapters own their SQL,
// identity winners, policy/visibility checks and bounded result projections.
type InspectionCatalog struct {
	root, layout string
	timeout      time.Duration
	slots        chan struct{}
	owner        context.Context
	cancel       context.CancelFunc
	key          [32]byte
	mu           sync.Mutex
	sources      []InspectionSource
	identities   map[string]inspectionFiles
	version      InspectionVersion
	active       int
	maintenance  *InspectionMaintenance
	closed       bool
	changed      chan struct{}
}
type inspectionLease struct {
	catalog   *InspectionCatalog
	conn      *sql.Conn
	database  *sql.DB
	ctx       context.Context
	cancel    context.CancelFunc
	stopOwner func() bool
	version   InspectionVersion
	sources   []InspectionSource
	once      sync.Once
	err       error
}
type InspectionMaintenance struct {
	catalog    *InspectionCatalog
	generation uint64
	once       sync.Once
}

// Construction performs no filesystem access, SQLite opening or history work.
func NewInspectionCatalog(options InspectionCatalogOptions) (*InspectionCatalog, error) {
	if !filepath.IsAbs(options.Root) || filepath.Clean(options.Root) != options.Root || options.Root == string(filepath.Separator) || !roleID(options.LayoutID) || options.Generation == 0 || options.Generation > math.MaxInt64 {
		return nil, ErrInvalidDatabasePath
	}
	sources, set, err := inspectionInventory(options.LayoutID, options.Sources)
	if err != nil {
		return nil, err
	}
	if options.Concurrency == 0 {
		options.Concurrency = 2
	}
	if options.Concurrency < 1 || options.Concurrency > 4 {
		return nil, ErrInspectionSourceLimit
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.Timeout <= 0 || options.Timeout > 5*time.Minute {
		return nil, ErrInspectionSourceLimit
	}
	owner, cancel := context.WithCancel(context.Background())
	c := &InspectionCatalog{root: options.Root, layout: options.LayoutID, sources: sources, version: InspectionVersion{SourceSet: set, Generation: options.Generation}, timeout: options.Timeout, slots: make(chan struct{}, options.Concurrency), owner: owner, cancel: cancel, changed: make(chan struct{}), identities: make(map[string]inspectionFiles)}
	if _, err = rand.Read(c.key[:]); err != nil {
		cancel()
		return nil, err
	}
	return c, nil
}
func inspectionInventory(layout string, sources []InspectionSource) ([]InspectionSource, string, error) {
	if len(sources) < 1 || len(sources) > 128 {
		return nil, "", ErrInspectionSourceLimit
	}
	seen := map[string]bool{}
	for i, s := range sources {
		if !roleID(s.ID) || seen[s.ID] || s.Current != (i == len(sources)-1) {
			return nil, "", ErrInvalidDatabasePath
		}
		seen[s.ID] = true
		if s.Current {
			if s.RelativePath != "observations.db" || s.Database.Identity != observationSchemaIdentity || s.Database.Revision != 1 {
				return nil, "", ErrInvalidDatabasePath
			}
		} else {
			if strings.ContainsAny(s.ID, "/\\\x00") || s.ID == "." || s.ID == ".." || s.RelativePath != "archives/"+s.ID+"/runtime.db" || s.Database.Identity != schemaIdentity || (s.Database.Revision != 1 && s.Database.Revision != 2) {
				return nil, "", ErrInvalidDatabasePath
			}
			initialized, err := time.Parse(time.RFC3339Nano, s.Database.InitializedAt)
			if err != nil || initialized.IsZero() || initialized.Location() != time.UTC {
				return nil, "", ErrUnsupportedSchema
			}
		}
	}
	encoded, _ := json.Marshal(struct {
		Layout  string
		Sources []InspectionSource
	}{layout, sources})
	digest := sha256.Sum256(encoded)
	return slices.Clone(sources), hex.EncodeToString(digest[:]), nil
}
func (c *InspectionCatalog) Version() InspectionVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}
func (c *InspectionCatalog) Check(version InspectionVersion) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrInspectionUnavailable
	}
	if c.maintenance != nil || version != c.version {
		return ErrInspectionChanged
	}
	return nil
}
func (c *InspectionCatalog) notifyLocked() { close(c.changed); c.changed = make(chan struct{}) }
func (c *InspectionCatalog) Close(ctx context.Context) error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.cancel()
		c.notifyLocked()
	}
	for c.active != 0 || c.maintenance != nil {
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		c.mu.Lock()
	}
	c.mu.Unlock()
	return nil
}
func (c *InspectionCatalog) acquire(ctx context.Context) (lease *inspectionLease, result error) {
	if ctx == nil {
		return nil, ErrInspectionUnavailable
	}
	work, cancel := context.WithTimeout(ctx, c.timeout)
	stop := context.AfterFunc(c.owner, cancel)
	select {
	case c.slots <- struct{}{}:
	case <-work.Done():
		stop()
		cancel()
		return nil, work.Err()
	}
	c.mu.Lock()
	for c.maintenance != nil && !c.closed {
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-work.Done():
			<-c.slots
			stop()
			cancel()
			return nil, work.Err()
		case <-changed:
		}
		c.mu.Lock()
	}
	if c.closed {
		c.mu.Unlock()
		<-c.slots
		stop()
		cancel()
		return nil, ErrInspectionUnavailable
	}
	lease = &inspectionLease{catalog: c, ctx: work, cancel: cancel, stopOwner: stop, version: c.version, sources: slices.Clone(c.sources)}
	c.active++
	c.mu.Unlock()
	defer func() {
		if result != nil {
			result = errors.Join(result, lease.close())
			lease = nil
		}
	}()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return lease, err
	}
	lease.database = db
	db.SetMaxOpenConns(1)
	lease.conn, err = db.Conn(work)
	if err != nil {
		return lease, err
	}
	limit, err := sqlite.Limit(lease.conn, sqlite3.SQLITE_LIMIT_ATTACHED, -1)
	if err != nil {
		return lease, err
	}
	if len(lease.sources) > limit {
		return lease, ErrInspectionSourceLimit
	}
	// Limits belong to this physical query connection. Program/cache settings
	// are not asserted to be CPU/temp-disk quotas; deadline and concurrency bind
	// query ownership, and oversized inventories fail instead of being omitted.
	if _, err = sqlite.Limit(lease.conn, sqlite3.SQLITE_LIMIT_SQL_LENGTH, 1<<20); err != nil {
		return lease, err
	}
	if _, err = lease.conn.ExecContext(work, `PRAGMA busy_timeout=50;PRAGMA query_only=ON;PRAGMA temp_store=FILE;PRAGMA cache_size=-2048`); err != nil {
		return lease, err
	}
	for i, source := range lease.sources {
		path := filepath.Join(c.root, filepath.FromSlash(source.RelativePath))
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || physical != path {
			return lease, ErrInspectionUnavailable
		}

		before, err := inspectionSourceFiles(path)
		if err != nil {
			return lease, err
		}
		if err = c.rememberSource(lease.version, source, before); err != nil {
			return lease, err
		}

		location := url.URL{Scheme: "file", Path: path}
		query := location.Query()
		query.Set("mode", "ro")
		location.RawQuery = query.Encode()
		alias := fmt.Sprintf("s%d", i)
		if _, err = lease.conn.ExecContext(work, `ATTACH DATABASE ? AS `+alias, location.String()); err != nil {
			return lease, errors.Join(ErrInspectionUnavailable, err)
		}
		if err = inspectAttachedObservation(work, lease.conn, alias, c.layout, source); err != nil {
			return lease, errors.Join(ErrInspectionUnavailable, err)
		}

		after, err := inspectionSourceFiles(path)
		if err != nil || !sameInspectionSourceFiles(before, after, source.Current) {
			return lease, ErrInspectionChanged
		}

	}
	if err = c.Check(lease.version); err != nil {
		return lease, err
	}
	return lease, nil
}
func (l *inspectionLease) close() error {
	l.once.Do(func() {
		l.stopOwner()
		l.cancel()
		if l.conn != nil {
			l.err = errors.Join(l.err, l.conn.Close())
		}
		if l.database != nil {
			l.err = errors.Join(l.err, l.database.Close())
		}
		// Cancellation alone never releases physical source ownership.
		<-l.catalog.slots
		l.catalog.mu.Lock()
		l.catalog.active--
		l.catalog.notifyLocked()
		l.catalog.mu.Unlock()
	})
	return l.err
}

// Task5 supplies a durably advanced generation before requesting maintenance.
// The reservation invalidates old continuations immediately, but physical work
// cannot begin until every real read lease closes. Cancellation never rewinds it.
func (c *InspectionCatalog) BeginMaintenance(ctx context.Context, generation uint64) (*InspectionMaintenance, error) {
	c.mu.Lock()
	if c.closed || c.maintenance != nil || generation <= c.version.Generation || generation > math.MaxInt64 {
		c.mu.Unlock()
		return nil, ErrInspectionChanged
	}
	token := &InspectionMaintenance{catalog: c, generation: generation}
	c.maintenance = token
	c.version.Generation = generation
	c.identities = make(map[string]inspectionFiles)
	c.notifyLocked()
	for c.active != 0 {
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			token.Close()
			return nil, ctx.Err()
		case <-c.owner.Done():
			token.Close()
			return nil, ErrInspectionUnavailable
		case <-changed:
		}
		c.mu.Lock()
	}
	c.mu.Unlock()
	return token, nil
}
func (m *InspectionMaintenance) Publish(sources []InspectionSource) error {
	inventory, set, err := inspectionInventory(m.catalog.layout, sources)
	if err != nil {
		return err
	}
	c := m.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maintenance != m || c.active != 0 || c.version.Generation != m.generation {
		return ErrInspectionChanged
	}
	c.sources = inventory
	c.version.SourceSet = set
	return nil
}
func (m *InspectionMaintenance) Close() error {
	m.once.Do(func() {
		c := m.catalog
		c.mu.Lock()
		if c.maintenance == m {
			c.maintenance = nil
			c.notifyLocked()
		}
		c.mu.Unlock()
	})
	return nil
}

func inspectAttachedObservation(ctx context.Context, conn *sql.Conn, alias, layout string, source InspectionSource) error {
	var state SchemaState
	if err := conn.QueryRowContext(ctx, `SELECT schema_identity,schema_revision,initialized_at FROM `+alias+`.runtime_metadata WHERE singleton=1`).Scan(&state.Identity, &state.Revision, &state.InitializedAt); err != nil {
		return err
	}
	if state.Identity != source.Database.Identity || state.Revision != source.Database.Revision || source.Database.InitializedAt != "" && state.InitializedAt != source.Database.InitializedAt {
		return ErrUnsupportedSchema
	}
	if source.Current {
		var actualLayout, actualSource string
		if err := conn.QueryRowContext(ctx, `SELECT layout_id,archive_id FROM `+alias+`.observation_storage_state WHERE singleton=1`).Scan(&actualLayout, &actualSource); err != nil || actualLayout != layout || actualSource != source.ID {
			return ErrUnsupportedSchema
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT type,name,tbl_name,coalesce(sql,'') FROM `+alias+`.sqlite_schema WHERE name NOT GLOB 'sqlite_*' ORDER BY type,name`)
	if err != nil {
		return err
	}
	var observed []schemaObject
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.kind, &object.name, &object.table, &object.statement); err != nil {
			rows.Close()
			return err
		}
		observed = append(observed, object)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	schemas := []string{schemaV1SQL}
	if source.Current {
		schemas = []string{observationSchemaSQL}
	} else if source.Database.Revision == 2 {
		schemas = []string{schemaV2SQL, schemaV1SQL + "\n" + migration001To002SQL}
	}
	for _, ddl := range schemas {
		reference, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return err
		}
		reference.SetMaxOpenConns(1)
		if _, err = reference.ExecContext(ctx, ddl); err != nil {
			reference.Close()
			return err
		}
		expected, err := schemaObjects(ctx, reference)
		closed := reference.Close()
		if err = errors.Join(err, closed); err != nil {
			return err
		}
		if slices.Equal(observed, expected) {
			return nil
		}
	}
	return ErrUnsupportedSchema
}

type inspectionFiles struct{ database, wal os.FileInfo }

func inspectionSourceFiles(path string) (inspectionFiles, error) {
	var result inspectionFiles
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return result, ErrInspectionUnavailable
	}
	result.database = info
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return result, ErrInspectionUnavailable
		}
		if suffix == "-wal" {
			result.wal = info
		}
	}
	return result, nil
}
func sameInspectionSourceFiles(a, b inspectionFiles, current bool) bool {
	if !os.SameFile(a.database, b.database) {
		return false
	}
	if current {
		return true
	}
	if a.database.Size() != b.database.Size() || !a.database.ModTime().Equal(b.database.ModTime()) {
		return false
	}
	if a.wal == nil || b.wal == nil {
		return a.wal == nil && b.wal == nil
	}
	return os.SameFile(a.wal, b.wal) && a.wal.Size() == b.wal.Size() && a.wal.ModTime().Equal(b.wal.ModTime())
}
func (c *InspectionCatalog) rememberSource(version InspectionVersion, source InspectionSource, files inspectionFiles) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.maintenance != nil || c.version != version {
		return ErrInspectionChanged
	}
	if previous, ok := c.identities[source.ID]; ok && !sameInspectionSourceFiles(previous, files, source.Current) {
		return ErrInspectionChanged
	}
	c.identities[source.ID] = files
	return nil
}
