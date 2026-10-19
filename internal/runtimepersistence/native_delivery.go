package runtimepersistence

import (
	"context"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Physical native delivery proof has no observation sequence or recording fence.
// Constants are reconstructed here so ordinary audit records cannot become receipts.
const nativeDeliverySelect = `SELECT 1,attempt_id,connection_id,'client_tool_service',payload_class,
 'client_operation',operation_id,'','core','',target_origin,policy_id,policy_revision,
 'environment',rule_id,proxy_id,proxy_revision,'',0,selection_source,default_revision,0,
 started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in,byte_counts_known
 FROM native_tool_deliveries`

func appendNativeDelivery(ctx context.Context, tx egressAttemptWriter, a egressaudit.Attempt) (egressaudit.Record, error) {
	d := a.Decision()
	if a.Purpose() != egressaudit.EgressPurpose("client_tool_service") || a.Parent().Kind != egressaudit.ParentKind("client_operation") || a.Parent().ExchangeID != "" || a.Caller() != egressaudit.CallerKind("core") || a.CallerID() != "" || d.Authority != egressaudit.PolicyAuthorityKind("environment") || d.AccountID != "" || d.AccountSettingsRevision != 0 || a.ReusedTransport() || a.Terminal() {
		return egressaudit.Record{}, nativetool.ErrInvalid
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO native_tool_deliveries(attempt_id,operation_id,connection_id,payload_class,target_origin,policy_id,policy_revision,rule_id,proxy_id,proxy_revision,selection_source,default_revision,started_at_unix_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID(), a.Parent().ID, a.ConnectionID(), a.PayloadClass(), a.TargetOrigin(), d.PolicyID, d.PolicyRevision, d.RuleID, d.ProxyID, d.ProxyRevision, d.SelectionSource, d.DefaultRevision, a.StartedAt().UnixMilli())
	return egressaudit.Record{Attempt: a}, err
}

func completeNativeDelivery(ctx context.Context, tx egressAttemptWriter, a egressaudit.Attempt) (egressaudit.Record, error) {
	if !a.Terminal() {
		return egressaudit.Record{}, nativetool.ErrInvalid
	}
	current, err := scanEgressAttempt(tx.QueryRowContext(ctx, nativeDeliverySelect+` WHERE attempt_id=?`, a.ID()))
	if err != nil {
		return egressaudit.Record{}, err
	}
	if current.Attempt.Terminal() {
		if current.Attempt != a {
			return egressaudit.Record{}, nativetool.ErrConflict
		}
		return current, nil
	}
	expected, err := current.Attempt.Finish(egressaudit.TerminalInput{Outcome: a.Outcome(), ErrorClass: a.ErrorClass(), BytesIn: a.BytesIn(), BytesOut: a.BytesOut(), UnobservedByteCounts: !a.ByteCountsKnown(), CompletedAt: a.CompletedAt()})
	if err != nil || expected != a {
		return egressaudit.Record{}, nativetool.ErrInvalid
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_tool_deliveries SET completed_at_unix_ms=?,outcome=?,error_class=?,bytes_out=?,bytes_in=?,byte_counts_known=? WHERE attempt_id=? AND completed_at_unix_ms IS NULL AND outcome=''`, a.CompletedAt().UnixMilli(), a.Outcome(), a.ErrorClass(), a.BytesOut(), a.BytesIn(), a.ByteCountsKnown(), a.ID())
	if err != nil {
		return egressaudit.Record{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return egressaudit.Record{}, err
	}
	if count != 1 {
		return egressaudit.Record{}, nativetool.ErrConflict
	}
	return egressaudit.Record{Attempt: a}, nil
}
