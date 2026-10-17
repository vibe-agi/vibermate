package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"time"
)

type ObservationOptions struct {
	DatabasePath, LayoutID, ArchiveID   string
	BusyTimeout, CommitReconcileTimeout time.Duration
	ContentLimits                       *exchangecontent.SourceLimits
}

type ObservationStore struct{ store *Store }

func OpenObservations(ctx context.Context, o ObservationOptions) (*ObservationStore, error) {
	if !roleID(o.LayoutID) || !roleID(o.ArchiveID) {
		return nil, ErrInvalidDatabasePath
	}
	role := &roleSchema{identity: observationSchemaIdentity, revision: 1, ddl: observationSchemaSQL, layout: o.LayoutID, archive: o.ArchiveID}
	s, err := openStore(ctx, Options{DatabasePath: o.DatabasePath, BusyTimeout: o.BusyTimeout, CommitReconcileTimeout: o.CommitReconcileTimeout, ContentLimits: o.ContentLimits}, role)
	if err != nil {
		return nil, err
	}
	return &ObservationStore{store: s}, nil
}

func (s *ObservationStore) Shutdown(ctx context.Context) error { return s.store.Shutdown(ctx) }

func (s *ObservationStore) SchemaStateReader() SchemaStateReader { return s.store.repo }

func (s *ObservationStore) Settings(ctx context.Context) (DatabaseSettings, error) {
	return s.store.Settings(ctx)
}
