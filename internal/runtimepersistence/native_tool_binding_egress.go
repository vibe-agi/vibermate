package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func loadNativeBindingEgress(ctx context.Context, tx *sql.Tx, id nativetool.ID, b *nativeBindingRow) error {
	var digest []byte
	var version, revision int64
	var raw, source string
	err := tx.QueryRowContext(ctx, `SELECT binding_digest,binding_version,profile_json,selection_source,default_revision FROM native_tool_binding_egress WHERE operation_id=?`, id).Scan(&digest, &version, &raw, &source, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		b.bindingVersion = 1
		return nil
	}
	if err != nil {
		return err
	}
	if version != 2 || revision < 0 || string(digest) != string(b.digest[:]) {
		return nativetool.ErrInvalid
	}
	var choice egressprofile.Choice
	if len(raw) > nativetool.MaxEffectiveProfileJSONBytes || json.Unmarshal([]byte(raw), &choice) != nil {
		return nativetool.ErrInvalid
	}
	profile, explicit := choice.ExplicitProfile()
	canonical, err := json.Marshal(profile)
	if err != nil || !explicit || string(canonical) != raw {
		return nativetool.ErrInvalid
	}
	resolved, err := egressprofile.RestoreResolved(profile, egressprofile.SelectionSource(source), uint64(revision))
	if err != nil || resolved.Source() == egressprofile.SelectionAccount || resolved.Source() == egressprofile.SelectionTransport {
		return nativetool.ErrInvalid
	}
	b.bindingVersion, b.resolved = 2, resolved
	return nil
}
