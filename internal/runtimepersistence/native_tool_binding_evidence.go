package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func nativeStoredDigest(raw []byte) *nativetool.Digest {
	if raw == nil {
		return nil
	}
	if len(raw) != 32 {
		return nil
	}
	var d nativetool.Digest
	copy(d[:], raw)
	return &d
}

func verifyNativeBindingEvidence(ctx context.Context, tx *sql.Tx, op nativetool.Operation, b nativeBindingRow, e environment.Environment) error {
	if e.ClientToolAccess == nil || e.ClientToolAccess.Route == nil {
		return nativetool.ErrInvalid
	}
	var session, kind string
	var rpc, request []byte
	err := tx.QueryRowContext(ctx, `SELECT session_id,request_kind,rpc_id_digest,request_digest FROM native_tool_inbound WHERE operation_id=?`, op.ID).Scan(&session, &kind, &rpc, &request)
	bodyless := op.Kind == nativetool.RequestGetStream || op.Kind == nativetool.RequestDeleteSession
	if bodyless {
		if !errors.Is(err, sql.ErrNoRows) {
			return nativetool.ErrInvalid
		}
	} else {
		if err != nil || session != string(b.session) || kind != string(op.Kind) || len(rpc) != 32 || string(request) != string(b.request[:]) {
			return nativetool.ErrInvalid
		}
	}
	evidence := nativetool.BindingEvidence{Kind: op.Kind, Capture: op.Capture, Environment: op.EnvironmentID, EnvironmentRevision: op.EnvironmentRevision, EnvironmentDigest: op.EnvironmentDigest, Route: *e.ClientToolAccess.Route, Recording: op.Recording, Policy: e.EffectivePolicySet(), Assignment: b.assignment, Owner: b.owner, Session: b.session, SessionRevision: b.sessionRevision, CatalogGeneration: b.generation, Catalog: nativeStoredDigest(b.catalog), Parent: b.parent, Server: nativeStoredDigest(b.server), Request: b.request, Params: b.params, Group: b.group, Replay: nativeStoredDigest(b.replay), Name: b.name, RPCID: nativeStoredDigest(rpc), CancelledRPCID: nativeStoredDigest(b.cancel)}
	switch b.bindingVersion {
	case 1:
		return nativetool.VerifyBindingEvidence(evidence, b.digest)
	case 2:
		return nativetool.VerifyBindingEvidenceV2(evidence, b.resolved, b.digest)
	default:
		return nativetool.ErrInvalid
	}
}
