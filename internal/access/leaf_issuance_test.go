package access_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
)

func TestLeafIssuanceAdmissionIsProjectionOwnedAndOneUse(t *testing.T) {
	t.Parallel()

	projection, _, plan := leafProjection(t, "access-leaf-admission")
	binding := plan.IngressBinding()
	intent := leafIntent(t, certidentity.InitialRootRevision, binding)
	admission, err := projection.AdmitLeaf(intent)
	if err != nil {
		t.Fatalf("admit current leaf: %v", err)
	}
	request, err := admission.ClaimForIssuance()
	if err != nil {
		t.Fatalf("claim leaf admission: %v", err)
	}
	if request.RootRevision() != certidentity.InitialRootRevision ||
		request.AccessID() != binding.AccessID() ||
		request.AgentEndpointID() != binding.AgentEndpointID() ||
		request.AgentEndpointRevision() != binding.AgentEndpointRevision() ||
		request.ClientOrigin() != binding.ClientOrigin() ||
		request.SAN().Value() != binding.ClientOrigin().TLSServerName() ||
		request.Algorithm() != certidentity.LeafKeyAlgorithmECDSAP256 ||
		!admission.Cacheable() {
		t.Fatalf("claimed leaf request is incomplete: %+v", request)
	}
	copy := admission
	if _, err := copy.ClaimForIssuance(); !errors.Is(
		err,
		access.ErrLeafAdmissionConsumed,
	) {
		t.Fatalf("replayed leaf admission error = %v", err)
	}
	if _, err := (access.LeafIssuanceAdmission{}).ClaimForIssuance(); !errors.Is(err, access.ErrLeafIssuanceUnauthorized) {
		t.Fatalf("zero leaf admission error = %v", err)
	}
}

func TestLeafIssuancePublicationIsTheRevocationCut(t *testing.T) {
	t.Parallel()

	projection, invalidator, first := leafProjection(
		t,
		"access-leaf-revocation",
	)
	oldBinding := first.IngressBinding()
	oldAdmission, err := projection.AdmitLeaf(
		leafIntent(t, certidentity.InitialRootRevision, oldBinding),
	)
	if err != nil {
		t.Fatal(err)
	}

	compiler := testCompiler(t)
	secondAggregate := testAggregate(
		t,
		first.AccessID(),
		2,
		"Endpoint revision replacement",
	)
	second, err := compiler.Compile(secondAggregate)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Publish(second); err != nil {
		t.Fatalf("publish endpoint replacement: %v", err)
	}
	if oldAdmission.Cacheable() {
		t.Fatal("pre-cut admission remained cacheable after endpoint replacement")
	}
	if _, err := oldAdmission.ClaimForIssuance(); err != nil {
		t.Fatalf("pre-cut admission could not complete after publication: %v", err)
	}
	if _, err := projection.AdmitLeaf(
		leafIntent(t, certidentity.InitialRootRevision, oldBinding),
	); !errors.Is(err, access.ErrLeafIssuanceUnauthorized) {
		t.Fatalf("post-cut stale admission error = %v", err)
	}

	invalidations := invalidator.snapshot()
	if len(invalidations) != 1 ||
		invalidations[0].AccessID() != first.AccessID() ||
		invalidations[0].AgentEndpointID() != oldBinding.AgentEndpointID() ||
		invalidations[0].AgentEndpointRevision() !=
			oldBinding.AgentEndpointRevision() {
		t.Fatalf("leaf invalidations = %+v", invalidations)
	}
	currentBinding, err := projection.ResolveClientOrigin(
		second.AgentEndpoint().ClientOrigin,
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err := projection.AdmitLeaf(
		leafIntent(t, certidentity.InitialRootRevision, currentBinding),
	)
	if err != nil || !current.Cacheable() {
		t.Fatalf("current endpoint admission cacheable=%t error=%v", current.Cacheable(), err)
	}
}

func TestLeafIssuanceRejectsStaleRootAndProductionIP(t *testing.T) {
	t.Parallel()

	projection, _, plan := leafProjection(t, "access-leaf-rejections")
	binding := plan.IngressBinding()
	if _, err := projection.AdmitLeaf(
		leafIntent(t, certidentity.RootRevision(2), binding),
	); !errors.Is(err, access.ErrLeafIssuanceUnauthorized) {
		t.Fatalf("stale Root revision admission error = %v", err)
	}

	compiler := testCompiler(t)
	ipAggregate := testAggregate(
		t,
		newAccessID(t, "access-leaf-ip"),
		1,
		"IP endpoint",
	)
	ipOrigin, err := access.NewClientOrigin("https://127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	ipAggregate.AgentEndpoint.ClientOrigin = ipOrigin
	ipPlan, err := compiler.Compile(ipAggregate)
	if err != nil {
		t.Fatal(err)
	}
	ipInvalidator := &recordingLeafCacheInvalidator{}
	ipProjection, err := access.NewSnapshotProjection(
		certidentity.InitialRootRevision,
		ipInvalidator,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ipProjection.Restore([]access.AccessPlanSnapshot{ipPlan}); err != nil {
		t.Fatal(err)
	}
	ipSAN, err := certidentity.NewIPAddress("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ipIntent, err := access.NewLeafIssuanceIntent(
		certidentity.InitialRootRevision,
		ipPlan.IngressBinding(),
		ipSAN,
		certidentity.LeafKeyAlgorithmECDSAP256,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ipProjection.AdmitLeaf(ipIntent); !errors.Is(
		err,
		access.ErrLeafSANUnsupported,
	) {
		t.Fatalf("IP leaf admission error = %v", err)
	}
}

func TestLeafIssuanceRejectsInvalidAndForeignConnectionEvidence(t *testing.T) {
	t.Parallel()

	projection, _, plan := leafProjection(t, "access-leaf-evidence")
	binding := plan.IngressBinding()
	validSAN, err := certidentity.NewDNSName(
		binding.ClientOrigin().TLSServerName(),
	)
	if err != nil {
		t.Fatal(err)
	}
	otherSAN, err := certidentity.NewDNSName("other.example.test")
	if err != nil {
		t.Fatal(err)
	}

	for name, construct := range map[string]func() error{
		"missing root revision": func() error {
			_, err := access.NewLeafIssuanceIntent(
				0,
				binding,
				validSAN,
				certidentity.LeafKeyAlgorithmECDSAP256,
			)
			return err
		},
		"missing binding": func() error {
			_, err := access.NewLeafIssuanceIntent(
				certidentity.InitialRootRevision,
				access.IngressBinding{},
				validSAN,
				certidentity.LeafKeyAlgorithmECDSAP256,
			)
			return err
		},
		"SAN and ClientOrigin mismatch": func() error {
			_, err := access.NewLeafIssuanceIntent(
				certidentity.InitialRootRevision,
				binding,
				otherSAN,
				certidentity.LeafKeyAlgorithmECDSAP256,
			)
			return err
		},
		"unsupported leaf algorithm": func() error {
			_, err := access.NewLeafIssuanceIntent(
				certidentity.InitialRootRevision,
				binding,
				validSAN,
				certidentity.LeafKeyAlgorithm("rsa-2048"),
			)
			return err
		},
	} {
		name, construct := name, construct
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := construct(); !errors.Is(
				err,
				access.ErrLeafIssuanceInvalid,
			) {
				t.Fatalf("connection evidence error = %v", err)
			}
		})
	}

	_, _, foreignPlan := leafProjection(t, "access-leaf-foreign")
	foreignIntent := leafIntent(
		t,
		certidentity.InitialRootRevision,
		foreignPlan.IngressBinding(),
	)
	if _, err := projection.AdmitLeaf(foreignIntent); !errors.Is(
		err,
		access.ErrLeafIssuanceUnauthorized,
	) {
		t.Fatalf("foreign Access and ClientOrigin admission error = %v", err)
	}
}

func TestProviderOnlyPublicationKeepsLeafAuthorizationCurrent(t *testing.T) {
	t.Parallel()

	projection, invalidator, first := leafProjection(
		t,
		"access-leaf-provider-only",
	)
	admission, err := projection.AdmitLeaf(
		leafIntent(
			t,
			certidentity.InitialRootRevision,
			first.IngressBinding(),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := testAggregate(t, first.AccessID(), 2, "Provider-only update")
	aggregate.AgentEndpoint.Revision = first.AgentEndpoint().Revision
	second, err := testCompiler(t).Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Publish(second); err != nil {
		t.Fatal(err)
	}
	if !admission.Cacheable() || len(invalidator.snapshot()) != 0 {
		t.Fatalf(
			"provider-only publish cacheable=%t invalidations=%+v",
			admission.Cacheable(),
			invalidator.snapshot(),
		)
	}
}

func TestDisabledAccessWithdrawalRevokesAdmissionAndRetainsRevisionTombstone(
	t *testing.T,
) {
	t.Parallel()

	projection, invalidator, first := leafProjection(
		t,
		"access-leaf-disabled",
	)
	oldBinding := first.IngressBinding()
	oldAdmission, err := projection.AdmitLeaf(
		leafIntent(t, certidentity.InitialRootRevision, oldBinding),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Withdraw(first.AccessID(), 2); err != nil {
		t.Fatalf("withdraw disabled Access: %v", err)
	}
	if oldAdmission.Cacheable() {
		t.Fatal("disabled Access admission remained cacheable")
	}
	if _, err := projection.ResolveAccess(first.AccessID()); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("disabled Access resolve error = %v", err)
	}
	if _, err := projection.AdmitLeaf(
		leafIntent(t, certidentity.InitialRootRevision, oldBinding),
	); !errors.Is(err, access.ErrLeafIssuanceUnauthorized) {
		t.Fatalf("disabled Access admission error = %v", err)
	}
	if err := projection.Publish(first); !errors.Is(
		err,
		access.ErrPublishedRevisionRegression,
	) {
		t.Fatalf("stale plan republish error = %v", err)
	}
	invalidations := invalidator.snapshot()
	if len(invalidations) != 1 ||
		invalidations[0].AccessID() != first.AccessID() {
		t.Fatalf("withdrawal invalidations = %+v", invalidations)
	}

	reenabled, err := testCompiler(t).Compile(
		testAggregate(t, first.AccessID(), 3, "Re-enabled Access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Publish(reenabled); err != nil {
		t.Fatalf("publish re-enabled Access: %v", err)
	}
	current, err := projection.ResolveAccess(first.AccessID())
	if err != nil || current.Revision() != 3 {
		t.Fatalf("re-enabled Access = revision:%d error:%v", current.Revision(), err)
	}
}

type recordingLeafCacheInvalidator struct {
	mu            sync.Mutex
	invalidations []access.LeafCacheInvalidation
}

func (invalidator *recordingLeafCacheInvalidator) InvalidateLeafCache(
	invalidation access.LeafCacheInvalidation,
) {
	invalidator.mu.Lock()
	defer invalidator.mu.Unlock()
	invalidator.invalidations = append(invalidator.invalidations, invalidation)
}

func (invalidator *recordingLeafCacheInvalidator) snapshot() []access.LeafCacheInvalidation {
	invalidator.mu.Lock()
	defer invalidator.mu.Unlock()
	return append([]access.LeafCacheInvalidation(nil), invalidator.invalidations...)
}

func leafProjection(
	t *testing.T,
	accessIDValue string,
) (*access.AtomicSnapshotProjection, *recordingLeafCacheInvalidator, access.AccessPlanSnapshot) {
	t.Helper()
	plan, err := testCompiler(t).Compile(
		testAggregate(t, newAccessID(t, accessIDValue), 1, "Leaf authority"),
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidator := &recordingLeafCacheInvalidator{}
	projection, err := access.NewSnapshotProjection(
		certidentity.InitialRootRevision,
		invalidator,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Restore([]access.AccessPlanSnapshot{plan}); err != nil {
		t.Fatal(err)
	}
	return projection, invalidator, plan
}

func leafIntent(
	t *testing.T,
	rootRevision certidentity.RootRevision,
	binding access.IngressBinding,
) access.LeafIssuanceIntent {
	t.Helper()
	san, err := certidentity.NewDNSName(binding.ClientOrigin().TLSServerName())
	if err != nil {
		t.Fatal(err)
	}
	intent, err := access.NewLeafIssuanceIntent(
		rootRevision,
		binding,
		san,
		certidentity.LeafKeyAlgorithmECDSAP256,
	)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}
