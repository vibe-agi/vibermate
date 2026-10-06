package runtimepersistence

import (
	_ "embed"
	"testing"
)

// Frozen exact prior Task2 snapshot from Task3 dispatch BASE2bfad38.
//
//go:embed testdata/pre_native_effective_egress_v2.sql
var preNativeEffectiveEgressV2SQL string

func TestNativeEffectiveEgressRefusesPriorPrivateV2Unchanged(t *testing.T) {
	assertOldEgressSchemaRefusedUnchanged(t, preNativeEffectiveEgressV2SQL, "5181ff2b720d7441408a0bf4959002f798029f36967d45d0fe41a94b63da52ca")
}
