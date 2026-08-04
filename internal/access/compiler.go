package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
)

var (
	ErrOwnershipViolation       = errors.New("Access resource ownership violation")
	ErrDanglingReference        = errors.New("Access resource reference is unresolved")
	ErrUnsupportedCardinality   = errors.New("Access plan cardinality is unsupported")
	ErrUnknownDialect           = errors.New("Access plan dialect is not in the catalog")
	ErrUnsupportedCodecPair     = errors.New("Access codec pair is unsupported")
	ErrCapabilityMismatch       = errors.New("ProviderTarget capability mismatch")
	ErrUnknownAuthDriver        = errors.New("AuthDriver is not in the catalog")
	ErrUnknownEgressMode        = errors.New("egress mode is not in the catalog")
	ErrUnknownPluginPlanMode    = errors.New("plugin plan mode is not in the catalog")
	ErrUnknownModelPolicyMode   = errors.New("model policy mode is not in the catalog")
	ErrUnknownTransportProfile  = errors.New("transport fingerprint profile is not in the catalog")
	ErrInvalidProviderTransport = errors.New("ProviderTarget transport is invalid")
	ErrAccessNotEnabled         = errors.New("Access is not enabled")
	ErrDuplicateResource        = errors.New("Access resource ID is duplicated")
	ErrUnsupportedPluginBinding = errors.New("plugin bindings are not enabled")
)

type PlanCapabilities struct {
	MaxEndpointProfiles          int
	MaxAccountBindings           int
	MaxRouteSets                 int
	AllowMultipleRouteCandidates bool
	AllowPluginBindings          bool
}

type CodecPairDefinition struct {
	ID                   CodecPairID
	Revision             Revision
	ClientDialect        Dialect
	ProviderDialect      Dialect
	ClientOperationIDs   []ClientOperationID
	RequiredCapabilities []ProviderCapability
}

type AuthDriverDefinition struct {
	Ref      AuthDriverRef
	Revision Revision
}

type EgressModeDefinition struct {
	Mode     EgressMode
	Revision Revision
}

type PluginPlanModeDefinition struct {
	Mode     PluginPlanMode
	Revision Revision
}

type ModelPolicyModeDefinition struct {
	Mode     ModelPolicyMode
	Revision Revision
}

type TransportFingerprintDefinition struct {
	Ref           TransportProfileRef
	Revision      Revision
	Source        TransportFingerprintSource
	HTTPTransport HTTPTransportKind
	ALPN          []ApplicationProtocol
	FallbackRefs  []TransportProfileRef
}

func ObservedClientH1TransportFingerprintDefinition() TransportFingerprintDefinition {
	return TransportFingerprintDefinition{
		Ref:           ObservedClientH1TransportProfileRef(),
		Revision:      1,
		Source:        TransportFingerprintObservedClient,
		HTTPTransport: HTTPTransportHTTP1,
		ALPN:          []ApplicationProtocol{ApplicationProtocolHTTP1},
		FallbackRefs: []TransportProfileRef{
			StandardH1TransportProfileRef(),
		},
	}
}

func StandardH1TransportFingerprintDefinition() TransportFingerprintDefinition {
	return TransportFingerprintDefinition{
		Ref:           StandardH1TransportProfileRef(),
		Revision:      1,
		Source:        TransportFingerprintStandard,
		HTTPTransport: HTTPTransportHTTP1,
		ALPN:          []ApplicationProtocol{ApplicationProtocolHTTP1},
	}
}

type CatalogOptions struct {
	Capabilities      PlanCapabilities
	ClientOperations  []ClientOperationDefinition
	CodecPairs        []CodecPairDefinition
	AuthDrivers       []AuthDriverDefinition
	EgressModes       []EgressModeDefinition
	PluginPlanModes   []PluginPlanModeDefinition
	ModelPolicyModes  []ModelPolicyModeDefinition
	TransportProfiles []TransportFingerprintDefinition
}

type codecPairKey struct {
	client   Dialect
	provider Dialect
}

// Catalog is an immutable, explicitly constructed compilation dependency.
type Catalog struct {
	initialized       bool
	capabilities      PlanCapabilities
	clientOperations  map[ClientOperationID]ClientOperationDefinition
	codecPairs        map[codecPairKey]CodecPairDefinition
	knownDialects     map[Dialect]struct{}
	authDrivers       map[AuthDriverRef]AuthDriverDefinition
	egressModes       map[EgressMode]EgressModeDefinition
	pluginPlanModes   map[PluginPlanMode]PluginPlanModeDefinition
	modelPolicyModes  map[ModelPolicyMode]ModelPolicyModeDefinition
	transportProfiles map[TransportProfileRef]TransportFingerprintDefinition
}

func NewCatalog(options CatalogOptions) (Catalog, error) {
	if options.Capabilities.MaxEndpointProfiles <= 0 ||
		options.Capabilities.MaxAccountBindings <= 0 ||
		options.Capabilities.MaxRouteSets <= 0 {
		return Catalog{}, errors.New("Access plan catalog limits must be positive")
	}
	catalog := Catalog{
		initialized:  true,
		capabilities: options.Capabilities,
		clientOperations: make(
			map[ClientOperationID]ClientOperationDefinition,
			len(options.ClientOperations),
		),
		codecPairs:       make(map[codecPairKey]CodecPairDefinition, len(options.CodecPairs)),
		knownDialects:    make(map[Dialect]struct{}),
		authDrivers:      make(map[AuthDriverRef]AuthDriverDefinition, len(options.AuthDrivers)),
		egressModes:      make(map[EgressMode]EgressModeDefinition, len(options.EgressModes)),
		pluginPlanModes:  make(map[PluginPlanMode]PluginPlanModeDefinition, len(options.PluginPlanModes)),
		modelPolicyModes: make(map[ModelPolicyMode]ModelPolicyModeDefinition, len(options.ModelPolicyModes)),
		transportProfiles: make(
			map[TransportProfileRef]TransportFingerprintDefinition,
			len(options.TransportProfiles),
		),
	}
	for _, definition := range options.ClientOperations {
		if err := definition.Validate(); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.clientOperations[definition.ID()]; duplicate {
			return Catalog{}, errors.New(
				"client operation definition is duplicated",
			)
		}
		cloned := cloneClientOperationDefinition(definition)
		catalog.clientOperations[cloned.ID()] = cloned
		catalog.knownDialects[cloned.ClientDialect()] = struct{}{}
	}
	for _, definition := range options.CodecPairs {
		if err := definition.ID.validate(); err != nil {
			return Catalog{}, err
		}
		if err := validateRevision("codec pair", definition.Revision); err != nil {
			return Catalog{}, err
		}
		if definition.ClientDialect == "" ||
			definition.ProviderDialect == "" ||
			len(definition.ClientOperationIDs) == 0 ||
			len(definition.RequiredCapabilities) == 0 {
			return Catalog{}, errors.New("codec pair definition is incomplete")
		}
		key := codecPairKey{
			client:   definition.ClientDialect,
			provider: definition.ProviderDialect,
		}
		if _, duplicate := catalog.codecPairs[key]; duplicate {
			return Catalog{}, errors.New("codec pair definition is duplicated")
		}
		definition.ClientOperationIDs = slices.Clone(
			definition.ClientOperationIDs,
		)
		sort.Slice(
			definition.ClientOperationIDs,
			func(left, right int) bool {
				return definition.ClientOperationIDs[left].String() <
					definition.ClientOperationIDs[right].String()
			},
		)
		for index, operationID := range definition.ClientOperationIDs {
			operation, exists := catalog.clientOperations[operationID]
			if !exists {
				return Catalog{}, fmt.Errorf(
					"codec pair client operation is unresolved: %q",
					operationID.String(),
				)
			}
			if operation.ClientDialect() != definition.ClientDialect {
				return Catalog{}, errors.New(
					"codec pair client operation dialect does not match",
				)
			}
			if operation.Kind() != ClientOperationSemantic {
				return Catalog{}, errors.New(
					"codec pair client operation is not semantic",
				)
			}
			if index > 0 &&
				operationID == definition.ClientOperationIDs[index-1] {
				return Catalog{}, errors.New(
					"codec pair client operation is duplicated",
				)
			}
		}
		definition.RequiredCapabilities = slices.Clone(definition.RequiredCapabilities)
		sortCapabilities(definition.RequiredCapabilities)
		if hasDuplicateCapabilities(definition.RequiredCapabilities) {
			return Catalog{}, errors.New("codec pair capabilities are duplicated")
		}
		catalog.codecPairs[key] = definition
		catalog.knownDialects[definition.ClientDialect] = struct{}{}
		catalog.knownDialects[definition.ProviderDialect] = struct{}{}
	}
	for _, definition := range options.AuthDrivers {
		if err := definition.Ref.validate(); err != nil {
			return Catalog{}, err
		}
		if err := validateRevision("AuthDriver", definition.Revision); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.authDrivers[definition.Ref]; duplicate {
			return Catalog{}, errors.New("AuthDriver definition is duplicated")
		}
		catalog.authDrivers[definition.Ref] = definition
	}
	for _, definition := range options.EgressModes {
		if definition.Mode == "" {
			return Catalog{}, errors.New("egress mode definition is empty")
		}
		if err := validateRevision("egress mode", definition.Revision); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.egressModes[definition.Mode]; duplicate {
			return Catalog{}, errors.New("egress mode definition is duplicated")
		}
		catalog.egressModes[definition.Mode] = definition
	}
	for _, definition := range options.PluginPlanModes {
		if definition.Mode == "" {
			return Catalog{}, errors.New("plugin plan mode definition is empty")
		}
		if err := validateRevision("plugin plan mode", definition.Revision); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.pluginPlanModes[definition.Mode]; duplicate {
			return Catalog{}, errors.New("plugin plan mode definition is duplicated")
		}
		catalog.pluginPlanModes[definition.Mode] = definition
	}
	for _, definition := range options.ModelPolicyModes {
		if definition.Mode == "" {
			return Catalog{}, errors.New("model policy mode definition is empty")
		}
		if err := validateRevision("model policy mode", definition.Revision); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.modelPolicyModes[definition.Mode]; duplicate {
			return Catalog{}, errors.New("model policy mode definition is duplicated")
		}
		catalog.modelPolicyModes[definition.Mode] = definition
	}
	for _, definition := range options.TransportProfiles {
		if err := validateTransportFingerprintDefinition(definition); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := catalog.transportProfiles[definition.Ref]; duplicate {
			return Catalog{}, errors.New(
				"transport fingerprint profile definition is duplicated",
			)
		}
		definition.ALPN = slices.Clone(definition.ALPN)
		definition.FallbackRefs = slices.Clone(definition.FallbackRefs)
		catalog.transportProfiles[definition.Ref] = definition
	}
	if err := validateTransportFallbacks(catalog.transportProfiles); err != nil {
		return Catalog{}, err
	}
	if len(catalog.clientOperations) == 0 ||
		len(catalog.codecPairs) == 0 ||
		len(catalog.authDrivers) == 0 ||
		len(catalog.egressModes) == 0 ||
		len(catalog.pluginPlanModes) == 0 ||
		len(catalog.modelPolicyModes) == 0 ||
		len(catalog.transportProfiles) == 0 {
		return Catalog{}, errors.New("Access plan catalog is incomplete")
	}
	return catalog, nil
}

// Compiler is a pure value compiler. It performs no I/O and owns no registry.
type Compiler struct {
	catalog Catalog
}

func NewCompiler(catalog Catalog) (*Compiler, error) {
	if !catalog.initialized {
		return nil, errors.New("Access plan catalog is not initialized")
	}
	return &Compiler{catalog: catalog}, nil
}

func (compiler *Compiler) Compile(aggregate Aggregate) (AccessPlanSnapshot, error) {
	if compiler == nil || !compiler.catalog.initialized {
		return AccessPlanSnapshot{}, errors.New("Access plan compiler is not initialized")
	}
	if err := aggregate.Validate(); err != nil {
		return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
	}
	candidate := canonicalizeAggregate(aggregate.Clone())
	if err := compiler.validateRelationships(candidate); err != nil {
		return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
	}

	// The plan is compiled once per RouteSet candidate. Candidate zero is what
	// every request starts with and what every existing accessor returns; the
	// rest exist so a policy that allows a further attempt has somewhere to
	// send it. Design 02 §12 names them.
	routeOrder, err := compiler.candidateOrder(candidate)
	if err != nil {
		return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
	}
	profile := routeOrder[0].profile
	target := routeOrder[0].target
	codecDefinition, err := compiler.resolveCodec(
		candidate.AgentEndpoint.ClientDialect,
		profile.BackendDialect,
	)
	if err != nil {
		return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
	}
	if target.Protocol != profile.BackendDialect {
		return AccessPlanSnapshot{}, errors.Join(
			ErrInvalidAccessPlan,
			fmt.Errorf(
				"%w: profile=%q backend=%q target=%q",
				ErrCapabilityMismatch,
				profile.ID.String(),
				profile.BackendDialect,
				target.Protocol,
			),
		)
	}
	if err := requireCapabilities(
		target.Capabilities,
		codecDefinition.RequiredCapabilities,
	); err != nil {
		return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
	}

	authDefinitions := make([]AuthDriverDefinition, 0, len(candidate.AccountBindings))
	for _, account := range candidate.AccountBindings {
		definition, exists := compiler.catalog.authDrivers[account.AuthDriverRef]
		if !exists {
			return AccessPlanSnapshot{}, errors.Join(
				ErrInvalidAccessPlan,
				fmt.Errorf("%w: %q", ErrUnknownAuthDriver, account.AuthDriverRef.String()),
			)
		}
		authDefinitions = append(authDefinitions, definition)
	}
	egressDefinition, exists := compiler.catalog.egressModes[candidate.EgressPolicy.Mode]
	if !exists {
		return AccessPlanSnapshot{}, errors.Join(
			ErrInvalidAccessPlan,
			fmt.Errorf("%w: %q", ErrUnknownEgressMode, candidate.EgressPolicy.Mode),
		)
	}
	if target.Origin.TransportKind() ==
		ProviderTransportLoopbackCleartext &&
		candidate.EgressPolicy.Mode != EgressModeDirect {
		return AccessPlanSnapshot{}, errors.Join(
			ErrInvalidAccessPlan,
			ErrInvalidProviderTransport,
			errors.New("loopback cleartext ProviderTarget requires direct egress"),
		)
	}
	pluginDefinition, exists := compiler.catalog.pluginPlanModes[candidate.PluginPlan.Mode]
	if !exists {
		return AccessPlanSnapshot{}, errors.Join(
			ErrInvalidAccessPlan,
			fmt.Errorf("%w: %q", ErrUnknownPluginPlanMode, candidate.PluginPlan.Mode),
		)
	}
	if len(candidate.PluginPlan.BindingIDs) != 0 &&
		!compiler.catalog.capabilities.AllowPluginBindings {
		return AccessPlanSnapshot{}, errors.Join(
			ErrInvalidAccessPlan,
			ErrUnsupportedPluginBinding,
		)
	}
	modelDefinition, exists := compiler.catalog.modelPolicyModes[profile.DefaultModelPolicy.Mode]
	if !exists {
		return AccessPlanSnapshot{}, errors.Join(
			ErrInvalidAccessPlan,
			fmt.Errorf("%w: %q", ErrUnknownModelPolicyMode, profile.DefaultModelPolicy.Mode),
		)
	}
	transportPlan, err := compiler.resolveTransportFingerprint(
		profile.TransportProfileRef,
	)
	if err != nil {
		return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
	}

	compiledTargets := make([]CompiledProviderTarget, 0, len(routeOrder))
	candidates := make([]CompiledCandidate, 0, len(routeOrder))
	for index, entry := range routeOrder {
		compiledTarget := compileTarget(entry.target)
		compiledTargets = append(compiledTargets, compiledTarget)
		if index == 0 {
			candidates = append(candidates, CompiledCandidate{
				profileID:     entry.profile.ID,
				target:        compiledTarget,
				codecPlan:     CodecPlan{},
				transportPlan: CompiledTransportFingerprintPlan{},
			})
			continue
		}
		// Every candidate is compiled against the same client dialect and the
		// same catalog. A candidate whose backend the plan cannot translate
		// to, or whose transport it cannot express, is not a fallback: it is a
		// plan that would fail differently.
		candidateCodec, codecErr := compiler.resolveCodec(
			candidate.AgentEndpoint.ClientDialect,
			entry.profile.BackendDialect,
		)
		if codecErr != nil {
			return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, codecErr)
		}
		if entry.target.Protocol != entry.profile.BackendDialect {
			return AccessPlanSnapshot{}, errors.Join(
				ErrInvalidAccessPlan,
				fmt.Errorf(
					"%w: profile=%q backend=%q target=%q",
					ErrCapabilityMismatch,
					entry.profile.ID.String(),
					entry.profile.BackendDialect,
					entry.target.Protocol,
				),
			)
		}
		if err := requireCapabilities(
			entry.target.Capabilities,
			candidateCodec.RequiredCapabilities,
		); err != nil {
			return AccessPlanSnapshot{}, errors.Join(ErrInvalidAccessPlan, err)
		}
		candidateTransport, transportErr := compiler.resolveTransportFingerprint(
			entry.profile.TransportProfileRef,
		)
		if transportErr != nil {
			return AccessPlanSnapshot{}, errors.Join(
				ErrInvalidAccessPlan,
				transportErr,
			)
		}
		if _, exists := compiler.catalog.modelPolicyModes[entry.profile.DefaultModelPolicy.Mode]; !exists {
			return AccessPlanSnapshot{}, errors.Join(
				ErrInvalidAccessPlan,
				fmt.Errorf(
					"%w: %q",
					ErrUnknownModelPolicyMode,
					entry.profile.DefaultModelPolicy.Mode,
				),
			)
		}
		candidates = append(candidates, CompiledCandidate{
			profileID:     entry.profile.ID,
			target:        compiledTarget,
			codecPlan:     compileCodecPlan(compiler, candidateCodec),
			transportPlan: candidateTransport,
		})
	}
	clientOperations := make(
		[]ClientOperationPlan,
		0,
		len(codecDefinition.ClientOperationIDs),
	)
	for _, operationID := range codecDefinition.ClientOperationIDs {
		definition := compiler.catalog.clientOperations[operationID]
		clientOperations = append(
			clientOperations,
			compileClientOperation(definition),
		)
	}
	codecPlan := CodecPlan{
		id:                   codecDefinition.ID,
		revision:             codecDefinition.Revision,
		clientDialect:        codecDefinition.ClientDialect,
		providerDialect:      codecDefinition.ProviderDialect,
		clientOperations:     clientOperations,
		requiredCapabilities: slices.Clone(codecDefinition.RequiredCapabilities),
	}
	pluginPlan := CompiledPluginPlan{
		revision:   candidate.PluginPlan.Revision,
		mode:       candidate.PluginPlan.Mode,
		bindingIDs: slices.Clone(candidate.PluginPlan.BindingIDs),
	}
	dependencies := buildDependencyRevisions(
		candidate,
		codecDefinition,
		clientOperations,
		authDefinitions,
		egressDefinition,
		pluginDefinition,
		modelDefinition,
		transportPlan,
	)
	candidates[0].codecPlan = codecPlan
	candidates[0].transportPlan = transportPlan
	snapshot := AccessPlanSnapshot{
		aggregate:       candidate,
		candidates:      candidates,
		compiledTargets: compiledTargets,
		codecPlan:       codecPlan,
		transportPlan:   transportPlan,
		pluginPlan:      pluginPlan,
		dependencies:    dependencies,
	}
	canonical, err := canonicalPlanBytes(snapshot)
	if err != nil {
		return AccessPlanSnapshot{}, fmt.Errorf("%w: canonicalize plan: %v", ErrInvalidAccessPlan, err)
	}
	snapshot.planHash = newPlanHash(canonical)
	return snapshot, nil
}

// routeCandidate pairs a RouteSet candidate with the target it names.
type routeCandidate struct {
	profile EndpointProfile
	target  ProviderTarget
}

// candidateOrder resolves the default RouteSet's candidates in the order the
// plan declares them. The order is the plan's, not a map's: a fallback that
// tried candidates in whatever order they hashed into would not be the
// fallback anybody configured.
func (compiler *Compiler) candidateOrder(
	aggregate Aggregate,
) ([]routeCandidate, error) {
	var routeSet RouteSet
	found := false
	for _, candidate := range aggregate.RouteSets {
		if candidate.ID == aggregate.Binding.DefaultRouteSetID {
			routeSet = candidate
			found = true
			break
		}
	}
	if !found || len(routeSet.CandidateProfileIDs) == 0 {
		return nil, fmt.Errorf("%w: default RouteSet", ErrDanglingReference)
	}
	order := make([]routeCandidate, 0, len(routeSet.CandidateProfileIDs))
	for _, profileID := range routeSet.CandidateProfileIDs {
		profile, profileFound := findProfile(aggregate, profileID)
		if !profileFound {
			return nil, fmt.Errorf(
				"%w: RouteSet profile %q",
				ErrDanglingReference,
				profileID.String(),
			)
		}
		target, targetFound := findTarget(aggregate, profile.TargetID)
		if !targetFound {
			return nil, fmt.Errorf(
				"%w: ProviderTarget %q",
				ErrDanglingReference,
				profile.TargetID.String(),
			)
		}
		order = append(order, routeCandidate{profile: profile, target: target})
	}
	return order, nil
}

func findProfile(
	aggregate Aggregate,
	profileID EndpointProfileID,
) (EndpointProfile, bool) {
	for _, profile := range aggregate.Profiles {
		if profile.ID == profileID {
			return profile, true
		}
	}
	return EndpointProfile{}, false
}

func findTarget(
	aggregate Aggregate,
	targetID ProviderTargetID,
) (ProviderTarget, bool) {
	for _, target := range aggregate.ProviderTargets {
		if target.ID == targetID {
			return target, true
		}
	}
	return ProviderTarget{}, false
}

func compileTarget(target ProviderTarget) CompiledProviderTarget {
	return CompiledProviderTarget{
		target:        target,
		basePath:      target.Origin.BasePath(),
		httpAuthority: target.Origin.HTTPAuthority(),
		networkHost:   target.Origin.NetworkHost(),
		tlsServerName: target.Origin.TLSServerName(),
		port:          target.Origin.Port(),
		transportKind: target.Origin.TransportKind(),
	}
}

func compileCodecPlan(
	compiler *Compiler,
	definition CodecPairDefinition,
) CodecPlan {
	operations := make([]ClientOperationPlan, 0, len(definition.ClientOperationIDs))
	for _, operationID := range definition.ClientOperationIDs {
		operations = append(
			operations,
			compileClientOperation(compiler.catalog.clientOperations[operationID]),
		)
	}
	return CodecPlan{
		id:                   definition.ID,
		revision:             definition.Revision,
		clientDialect:        definition.ClientDialect,
		providerDialect:      definition.ProviderDialect,
		clientOperations:     operations,
		requiredCapabilities: slices.Clone(definition.RequiredCapabilities),
	}
}

func compileClientOperation(
	definition ClientOperationDefinition,
) ClientOperationPlan {
	return ClientOperationPlan{
		id:             definition.id,
		revision:       definition.revision,
		clientDialect:  definition.clientDialect,
		methods:        slices.Clone(definition.methods),
		pathPattern:    definition.pathPattern,
		pathMatch:      definition.pathMatch,
		kind:           definition.kind,
		transport:      definition.transport,
		bodyKind:       definition.bodyKind,
		replayClass:    definition.replayClass,
		codecFeature:   definition.codecFeature,
		maxBodyBytes:   definition.maxBodyBytes,
		allowedQueries: slices.Clone(definition.allowedQueries),
		egressBearing:  definition.egressBearing,
	}
}

func validateTransportFingerprintDefinition(
	definition TransportFingerprintDefinition,
) error {
	if err := definition.Ref.validate(); err != nil {
		return err
	}
	if err := validateRevision(
		"transport fingerprint profile",
		definition.Revision,
	); err != nil {
		return err
	}
	switch definition.Source {
	case TransportFingerprintObservedClient,
		TransportFingerprintStandard:
	default:
		return errors.New("transport fingerprint source is invalid")
	}
	if definition.HTTPTransport != HTTPTransportHTTP1 {
		return errors.New("transport fingerprint HTTP transport is unsupported")
	}
	if len(definition.ALPN) == 0 {
		return errors.New("transport fingerprint ALPN policy is empty")
	}
	seenALPN := make(map[ApplicationProtocol]struct{}, len(definition.ALPN))
	for _, protocol := range definition.ALPN {
		switch protocol {
		case ApplicationProtocolHTTP1:
		case ApplicationProtocolHTTP2:
			return errors.New(
				"HTTP/2 fingerprint transport is not implemented by the current runtime",
			)
		default:
			return errors.New("transport fingerprint ALPN value is invalid")
		}
		if _, duplicate := seenALPN[protocol]; duplicate {
			return errors.New("transport fingerprint ALPN value is duplicated")
		}
		seenALPN[protocol] = struct{}{}
	}
	seenFallback := make(map[TransportProfileRef]struct{}, len(definition.FallbackRefs))
	for _, reference := range definition.FallbackRefs {
		if err := reference.validate(); err != nil {
			return err
		}
		if reference == definition.Ref {
			return errors.New("transport fingerprint profile falls back to itself")
		}
		if _, duplicate := seenFallback[reference]; duplicate {
			return errors.New(
				"transport fingerprint fallback reference is duplicated",
			)
		}
		seenFallback[reference] = struct{}{}
	}
	return nil
}

func validateTransportFallbacks(
	definitions map[TransportProfileRef]TransportFingerprintDefinition,
) error {
	for reference := range definitions {
		visiting := make(map[TransportProfileRef]bool)
		var visit func(TransportProfileRef) error
		visit = func(current TransportProfileRef) error {
			if visiting[current] {
				return errors.New("transport fingerprint fallback chain contains a cycle")
			}
			definition, exists := definitions[current]
			if !exists {
				return fmt.Errorf(
					"%w: %q",
					ErrUnknownTransportProfile,
					current.String(),
				)
			}
			visiting[current] = true
			for _, fallback := range definition.FallbackRefs {
				if err := visit(fallback); err != nil {
					return err
				}
			}
			delete(visiting, current)
			return nil
		}
		if err := visit(reference); err != nil {
			return err
		}
	}
	return nil
}

func (compiler *Compiler) resolveTransportFingerprint(
	reference TransportProfileRef,
) (CompiledTransportFingerprintPlan, error) {
	requested, exists := compiler.catalog.transportProfiles[reference]
	if !exists {
		return CompiledTransportFingerprintPlan{}, fmt.Errorf(
			"%w: %q",
			ErrUnknownTransportProfile,
			reference.String(),
		)
	}
	plan := CompiledTransportFingerprintPlan{
		requested: compileTransportTemplate(requested),
	}
	seen := map[TransportProfileRef]struct{}{requested.Ref: {}}
	queue := slices.Clone(requested.FallbackRefs)
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if _, duplicate := seen[current]; duplicate {
			continue
		}
		seen[current] = struct{}{}
		definition := compiler.catalog.transportProfiles[current]
		plan.fallbacks = append(
			plan.fallbacks,
			compileTransportTemplate(definition),
		)
		queue = append(queue, definition.FallbackRefs...)
	}
	return plan, nil
}

func compileTransportTemplate(
	definition TransportFingerprintDefinition,
) TransportFingerprintTemplate {
	return TransportFingerprintTemplate{
		ref:           definition.Ref,
		revision:      definition.Revision,
		source:        definition.Source,
		httpTransport: definition.HTTPTransport,
		alpn:          slices.Clone(definition.ALPN),
	}
}

func (compiler *Compiler) validateRelationships(aggregate Aggregate) error {
	accessID := aggregate.Binding.ID
	if aggregate.Binding.Status != AccessStatusEnabled {
		return ErrAccessNotEnabled
	}
	if aggregate.AgentEndpoint.AccessID != accessID ||
		aggregate.AgentEndpoint.ID != aggregate.Binding.AgentEndpointID {
		return fmt.Errorf("%w: AgentEndpoint", ErrOwnershipViolation)
	}
	if len(aggregate.Profiles) > compiler.catalog.capabilities.MaxEndpointProfiles ||
		len(aggregate.AccountBindings) > compiler.catalog.capabilities.MaxAccountBindings ||
		len(aggregate.RouteSets) > compiler.catalog.capabilities.MaxRouteSets {
		return ErrUnsupportedCardinality
	}
	// One RouteSet, and one target and one account per profile. More than one
	// profile is what a second candidate is: an upstream a dropped attempt can
	// be sent to, named in advance rather than guessed at.
	if len(aggregate.Profiles) == 0 ||
		len(aggregate.ProviderTargets) != len(aggregate.Profiles) ||
		len(aggregate.AccountBindings) != len(aggregate.Profiles) ||
		len(aggregate.RouteSets) != 1 {
		return ErrUnsupportedCardinality
	}
	if len(aggregate.Profiles) > 1 &&
		!compiler.catalog.capabilities.AllowMultipleRouteCandidates {
		return ErrUnsupportedCardinality
	}

	profiles := make(map[EndpointProfileID]EndpointProfile, len(aggregate.Profiles))
	for _, profile := range aggregate.Profiles {
		if profile.AccessID != accessID {
			return fmt.Errorf("%w: EndpointProfile %q", ErrOwnershipViolation, profile.ID.String())
		}
		if _, duplicate := profiles[profile.ID]; duplicate {
			return fmt.Errorf("%w: EndpointProfile %q", ErrDuplicateResource, profile.ID.String())
		}
		profiles[profile.ID] = profile
	}
	if err := sameProfileSet(aggregate.Binding.ProfileIDs, profiles); err != nil {
		return err
	}

	targets := make(map[ProviderTargetID]ProviderTarget, len(aggregate.ProviderTargets))
	for _, target := range aggregate.ProviderTargets {
		if target.AccessID != accessID {
			return fmt.Errorf("%w: ProviderTarget %q", ErrOwnershipViolation, target.ID.String())
		}
		profile, exists := profiles[target.ProfileID]
		if !exists || profile.TargetID != target.ID {
			return fmt.Errorf("%w: ProviderTarget %q", ErrDanglingReference, target.ID.String())
		}
		if _, duplicate := targets[target.ID]; duplicate {
			return fmt.Errorf("%w: ProviderTarget %q", ErrDuplicateResource, target.ID.String())
		}
		targets[target.ID] = target
	}
	for _, profile := range aggregate.Profiles {
		if _, exists := targets[profile.TargetID]; !exists {
			return fmt.Errorf("%w: profile target %q", ErrDanglingReference, profile.TargetID.String())
		}
	}

	accounts := make(map[AccountBindingID]ProviderAccountBinding, len(aggregate.AccountBindings))
	for _, account := range aggregate.AccountBindings {
		if account.AccessID != accessID {
			return fmt.Errorf("%w: account %q", ErrOwnershipViolation, account.ID.String())
		}
		if _, exists := profiles[account.ProfileID]; !exists {
			return fmt.Errorf("%w: account profile %q", ErrDanglingReference, account.ProfileID.String())
		}
		if _, duplicate := accounts[account.ID]; duplicate {
			return fmt.Errorf("%w: account %q", ErrDuplicateResource, account.ID.String())
		}
		accounts[account.ID] = account
	}
	for _, profile := range aggregate.Profiles {
		if err := validateProfileAccounts(profile, accounts); err != nil {
			return err
		}
	}

	routeSets := make(map[RouteSetID]RouteSet, len(aggregate.RouteSets))
	for _, routeSet := range aggregate.RouteSets {
		if routeSet.AccessID != accessID {
			return fmt.Errorf("%w: RouteSet %q", ErrOwnershipViolation, routeSet.ID.String())
		}
		if len(routeSet.CandidateProfileIDs) != 1 &&
			!compiler.catalog.capabilities.AllowMultipleRouteCandidates {
			return ErrUnsupportedCardinality
		}
		for _, profileID := range routeSet.CandidateProfileIDs {
			profile, exists := profiles[profileID]
			if !exists {
				return fmt.Errorf("%w: RouteSet profile %q", ErrDanglingReference, profileID.String())
			}
			defaultAccount, exists := accounts[profile.DefaultAccountBindingID]
			if !exists || !defaultAccount.Enabled {
				return fmt.Errorf(
					"%w: RouteSet profile %q has a disabled default account",
					ErrInvalidAccessPlan,
					profileID.String(),
				)
			}
		}
		if _, duplicate := routeSets[routeSet.ID]; duplicate {
			return fmt.Errorf("%w: RouteSet %q", ErrDuplicateResource, routeSet.ID.String())
		}
		routeSets[routeSet.ID] = routeSet
	}
	if _, exists := routeSets[aggregate.Binding.DefaultRouteSetID]; !exists {
		return fmt.Errorf("%w: default RouteSet", ErrDanglingReference)
	}
	if aggregate.EgressPolicy.AccessID != accessID ||
		aggregate.EgressPolicy.ID != aggregate.Binding.EgressPolicyID {
		return fmt.Errorf("%w: AccessEgressPolicy", ErrOwnershipViolation)
	}
	if aggregate.PluginPlan.AccessID != accessID {
		return fmt.Errorf("%w: PluginPlan", ErrOwnershipViolation)
	}
	return nil
}

func (compiler *Compiler) resolveCodec(
	client Dialect,
	provider Dialect,
) (CodecPairDefinition, error) {
	if _, exists := compiler.catalog.knownDialects[client]; !exists {
		return CodecPairDefinition{}, fmt.Errorf("%w: client=%q", ErrUnknownDialect, client)
	}
	if _, exists := compiler.catalog.knownDialects[provider]; !exists {
		return CodecPairDefinition{}, fmt.Errorf("%w: provider=%q", ErrUnknownDialect, provider)
	}
	definition, exists := compiler.catalog.codecPairs[codecPairKey{
		client:   client,
		provider: provider,
	}]
	if !exists {
		return CodecPairDefinition{}, fmt.Errorf(
			"%w: client=%q provider=%q",
			ErrUnsupportedCodecPair,
			client,
			provider,
		)
	}
	return definition, nil
}

func canonicalizeAggregate(aggregate Aggregate) Aggregate {
	sort.Slice(aggregate.Binding.ProfileIDs, func(left, right int) bool {
		return aggregate.Binding.ProfileIDs[left].String() <
			aggregate.Binding.ProfileIDs[right].String()
	})
	sort.Slice(aggregate.Profiles, func(left, right int) bool {
		return aggregate.Profiles[left].ID.String() < aggregate.Profiles[right].ID.String()
	})
	for index := range aggregate.Profiles {
		sort.Slice(aggregate.Profiles[index].AccountBindingIDs, func(left, right int) bool {
			return aggregate.Profiles[index].AccountBindingIDs[left].String() <
				aggregate.Profiles[index].AccountBindingIDs[right].String()
		})
	}
	sort.Slice(aggregate.ProviderTargets, func(left, right int) bool {
		return aggregate.ProviderTargets[left].ID.String() <
			aggregate.ProviderTargets[right].ID.String()
	})
	for index := range aggregate.ProviderTargets {
		sortCapabilities(aggregate.ProviderTargets[index].Capabilities)
	}
	sort.Slice(aggregate.AccountBindings, func(left, right int) bool {
		return aggregate.AccountBindings[left].ID.String() <
			aggregate.AccountBindings[right].ID.String()
	})
	sort.Slice(aggregate.RouteSets, func(left, right int) bool {
		return aggregate.RouteSets[left].ID.String() < aggregate.RouteSets[right].ID.String()
	})
	return aggregate
}

func sameProfileSet(
	references []EndpointProfileID,
	profiles map[EndpointProfileID]EndpointProfile,
) error {
	if len(references) != len(profiles) {
		return fmt.Errorf("%w: AccessBinding profiles", ErrDanglingReference)
	}
	seen := make(map[EndpointProfileID]struct{}, len(references))
	for _, profileID := range references {
		if _, exists := profiles[profileID]; !exists {
			return fmt.Errorf("%w: profile %q", ErrDanglingReference, profileID.String())
		}
		if _, duplicate := seen[profileID]; duplicate {
			return fmt.Errorf("%w: profile reference %q", ErrDuplicateResource, profileID.String())
		}
		seen[profileID] = struct{}{}
	}
	return nil
}

func validateProfileAccounts(
	profile EndpointProfile,
	accounts map[AccountBindingID]ProviderAccountBinding,
) error {
	seen := make(map[AccountBindingID]struct{}, len(profile.AccountBindingIDs))
	for _, accountID := range profile.AccountBindingIDs {
		account, exists := accounts[accountID]
		if !exists {
			return fmt.Errorf("%w: account %q", ErrDanglingReference, accountID.String())
		}
		if account.ProfileID != profile.ID {
			return fmt.Errorf("%w: account %q", ErrOwnershipViolation, accountID.String())
		}
		if _, duplicate := seen[accountID]; duplicate {
			return fmt.Errorf("%w: account reference %q", ErrDuplicateResource, accountID.String())
		}
		seen[accountID] = struct{}{}
	}
	defaultAccount, exists := accounts[profile.DefaultAccountBindingID]
	if !exists || defaultAccount.ProfileID != profile.ID {
		return fmt.Errorf("%w: default account", ErrDanglingReference)
	}
	return nil
}

func requireCapabilities(
	available []ProviderCapability,
	required []ProviderCapability,
) error {
	if hasDuplicateCapabilities(available) {
		return fmt.Errorf("%w: ProviderTarget capabilities are duplicated", ErrCapabilityMismatch)
	}
	byCapability := make(map[ProviderCapability]struct{}, len(available))
	for _, capability := range available {
		byCapability[capability] = struct{}{}
	}
	for _, capability := range required {
		if _, exists := byCapability[capability]; !exists {
			return fmt.Errorf("%w: missing=%q", ErrCapabilityMismatch, capability)
		}
	}
	return nil
}

func sortCapabilities(capabilities []ProviderCapability) {
	sort.Slice(capabilities, func(left, right int) bool {
		return capabilities[left] < capabilities[right]
	})
}

func hasDuplicateCapabilities(capabilities []ProviderCapability) bool {
	for index := 1; index < len(capabilities); index++ {
		if capabilities[index] == capabilities[index-1] {
			return true
		}
	}
	return false
}

func buildDependencyRevisions(
	aggregate Aggregate,
	codec CodecPairDefinition,
	clientOperations []ClientOperationPlan,
	authDrivers []AuthDriverDefinition,
	egress EgressModeDefinition,
	plugin PluginPlanModeDefinition,
	model ModelPolicyModeDefinition,
	transport CompiledTransportFingerprintPlan,
) []DependencyRevision {
	dependencies := []DependencyRevision{
		{Kind: DependencyAccessBinding, ID: aggregate.Binding.ID.String(), Revision: aggregate.Binding.Revision},
		{Kind: DependencyAgentEndpoint, ID: aggregate.AgentEndpoint.ID.String(), Revision: aggregate.AgentEndpoint.Revision},
		{Kind: DependencyAccessEgressPolicy, ID: aggregate.EgressPolicy.ID.String(), Revision: aggregate.EgressPolicy.Revision},
		{Kind: DependencyPluginPlan, ID: aggregate.Binding.ID.String(), Revision: aggregate.PluginPlan.Revision},
		{Kind: DependencyCodecPair, ID: codec.ID.String(), Revision: codec.Revision},
		{Kind: DependencyEgressCapability, ID: string(egress.Mode), Revision: egress.Revision},
		{Kind: DependencyPluginPlanCapability, ID: string(plugin.Mode), Revision: plugin.Revision},
		{Kind: DependencyModelPolicyCapability, ID: string(model.Mode), Revision: model.Revision},
	}
	for _, operation := range clientOperations {
		dependencies = append(dependencies, DependencyRevision{
			Kind:     DependencyClientOperation,
			ID:       operation.id.String(),
			Revision: operation.revision,
		})
	}
	for _, template := range append(
		[]TransportFingerprintTemplate{transport.requested},
		transport.fallbacks...,
	) {
		dependencies = append(dependencies, DependencyRevision{
			Kind:     DependencyTransportFingerprint,
			ID:       template.ref.String(),
			Revision: template.revision,
		})
	}
	for _, profile := range aggregate.Profiles {
		dependencies = append(dependencies,
			DependencyRevision{
				Kind:     DependencyEndpointProfile,
				ID:       profile.ID.String(),
				Revision: profile.Revision,
			},
			DependencyRevision{
				Kind:     DependencyModelPolicy,
				ID:       profile.ID.String(),
				Revision: profile.DefaultModelPolicy.Revision,
			},
		)
	}
	for _, target := range aggregate.ProviderTargets {
		dependencies = append(dependencies, DependencyRevision{
			Kind:     DependencyProviderTarget,
			ID:       target.ID.String(),
			Revision: target.Revision,
		})
	}
	for _, account := range aggregate.AccountBindings {
		dependencies = append(dependencies, DependencyRevision{
			Kind:     DependencyAccountBinding,
			ID:       account.ID.String(),
			Revision: account.Revision,
		})
	}
	for _, routeSet := range aggregate.RouteSets {
		dependencies = append(dependencies, DependencyRevision{
			Kind:     DependencyRouteSet,
			ID:       routeSet.ID.String(),
			Revision: routeSet.Revision,
		})
	}
	for _, auth := range authDrivers {
		dependencies = append(dependencies, DependencyRevision{
			Kind:     DependencyAuthDriver,
			ID:       auth.Ref.String(),
			Revision: auth.Revision,
		})
	}
	sort.Slice(dependencies, func(left, right int) bool {
		if dependencies[left].Kind != dependencies[right].Kind {
			return dependencies[left].Kind < dependencies[right].Kind
		}
		if dependencies[left].ID != dependencies[right].ID {
			return dependencies[left].ID < dependencies[right].ID
		}
		return dependencies[left].Revision < dependencies[right].Revision
	})
	return dependencies
}

type canonicalPlan struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Binding       canonicalBinding       `json:"binding"`
	AgentEndpoint canonicalAgentEndpoint `json:"agentEndpoint"`
	Profiles      []canonicalProfile     `json:"profiles"`
	Targets       []canonicalTarget      `json:"targets"`
	Accounts      []canonicalAccount     `json:"accounts"`
	RouteSets     []canonicalRouteSet    `json:"routeSets"`
	Egress        canonicalEgress        `json:"egress"`
	Plugin        canonicalPlugin        `json:"plugin"`
	Codec         canonicalCodec         `json:"codec"`
	Transport     canonicalTransportPlan `json:"transport"`
	Dependencies  []canonicalDependency  `json:"dependencies"`
}

type canonicalBinding struct {
	ID                string   `json:"id"`
	Revision          Revision `json:"revision"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Status            string   `json:"status"`
	AgentEndpointID   string   `json:"agentEndpointId"`
	DefaultRouteSetID string   `json:"defaultRouteSetId"`
	ProfileIDs        []string `json:"profileIds"`
	EgressPolicyID    string   `json:"egressPolicyId"`
}

type canonicalAgentEndpoint struct {
	ID            string   `json:"id"`
	Revision      Revision `json:"revision"`
	AccessID      string   `json:"accessId"`
	ClientOrigin  string   `json:"clientOrigin"`
	ClientDialect string   `json:"clientDialect"`
}

type canonicalModelPolicy struct {
	Revision   Revision `json:"revision"`
	Mode       string   `json:"mode"`
	FixedModel string   `json:"fixedModel"`
	MappingRef string   `json:"mappingRef"`
}

type canonicalProfile struct {
	ID                      string               `json:"id"`
	Revision                Revision             `json:"revision"`
	AccessID                string               `json:"accessId"`
	Name                    string               `json:"name"`
	Description             string               `json:"description"`
	BackendDialect          string               `json:"backendDialect"`
	TargetID                string               `json:"targetId"`
	TransportProfileRef     string               `json:"transportProfileRef"`
	DefaultModelPolicy      canonicalModelPolicy `json:"defaultModelPolicy"`
	AccountBindingIDs       []string             `json:"accountBindingIds"`
	DefaultAccountBindingID string               `json:"defaultAccountBindingId"`
}

type canonicalTransportTemplate struct {
	Ref           string                `json:"ref"`
	Revision      Revision              `json:"revision"`
	Source        string                `json:"source"`
	HTTPTransport string                `json:"httpTransport"`
	ALPN          []ApplicationProtocol `json:"alpn"`
}

type canonicalTransportPlan struct {
	Requested canonicalTransportTemplate   `json:"requested"`
	Fallbacks []canonicalTransportTemplate `json:"fallbacks"`
}

type canonicalTarget struct {
	ID            string               `json:"id"`
	Revision      Revision             `json:"revision"`
	AccessID      string               `json:"accessId"`
	ProfileID     string               `json:"profileId"`
	Origin        string               `json:"origin"`
	BasePath      string               `json:"basePath"`
	HTTPAuthority string               `json:"httpAuthority"`
	TLSServerName string               `json:"tlsServerName"`
	Protocol      string               `json:"protocol"`
	Capabilities  []ProviderCapability `json:"capabilities"`
}

type canonicalAccount struct {
	ID            string   `json:"id"`
	Revision      Revision `json:"revision"`
	AccessID      string   `json:"accessId"`
	ProfileID     string   `json:"profileId"`
	Label         string   `json:"label"`
	SecretRef     string   `json:"secretRef"`
	AuthDriverRef string   `json:"authDriverRef"`
	Enabled       bool     `json:"enabled"`
}

type canonicalRouteSet struct {
	ID                  string   `json:"id"`
	Revision            Revision `json:"revision"`
	AccessID            string   `json:"accessId"`
	CandidateProfileIDs []string `json:"candidateProfileIds"`
}

type canonicalEgress struct {
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
	AccessID string   `json:"accessId"`
	Mode     string   `json:"mode"`
}

type canonicalPlugin struct {
	Revision   Revision `json:"revision"`
	AccessID   string   `json:"accessId"`
	Mode       string   `json:"mode"`
	BindingIDs []string `json:"bindingIds"`
}

type canonicalCodec struct {
	ID                   string                     `json:"id"`
	Revision             Revision                   `json:"revision"`
	ClientDialect        string                     `json:"clientDialect"`
	ProviderDialect      string                     `json:"providerDialect"`
	ClientOperations     []canonicalClientOperation `json:"clientOperations"`
	RequiredCapabilities []ProviderCapability       `json:"requiredCapabilities"`
}

type canonicalClientOperation struct {
	ID             string   `json:"id"`
	Revision       Revision `json:"revision"`
	ClientDialect  string   `json:"clientDialect"`
	Methods        []string `json:"methods"`
	PathPattern    string   `json:"pathPattern"`
	PathMatch      string   `json:"pathMatch"`
	Kind           string   `json:"kind"`
	Transport      string   `json:"transport"`
	BodyKind       string   `json:"bodyKind"`
	ReplayClass    string   `json:"replayClass"`
	CodecFeature   string   `json:"codecFeature"`
	MaxBodyBytes   int64    `json:"maxBodyBytes"`
	AllowedQueries []string `json:"allowedQueries"`
	EgressBearing  bool     `json:"egressBearing"`
}

type canonicalDependency struct {
	Kind     string   `json:"kind"`
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
}

func canonicalPlanBytes(snapshot AccessPlanSnapshot) ([]byte, error) {
	binding := snapshot.aggregate.Binding
	canonical := canonicalPlan{
		SchemaVersion: 4,
		Binding: canonicalBinding{
			ID:                binding.ID.String(),
			Revision:          binding.Revision,
			Name:              binding.Name,
			Description:       binding.Description,
			Status:            string(binding.Status),
			AgentEndpointID:   binding.AgentEndpointID.String(),
			DefaultRouteSetID: binding.DefaultRouteSetID.String(),
			ProfileIDs:        endpointProfileIDStrings(binding.ProfileIDs),
			EgressPolicyID:    binding.EgressPolicyID.String(),
		},
		AgentEndpoint: canonicalAgentEndpoint{
			ID:            snapshot.aggregate.AgentEndpoint.ID.String(),
			Revision:      snapshot.aggregate.AgentEndpoint.Revision,
			AccessID:      snapshot.aggregate.AgentEndpoint.AccessID.String(),
			ClientOrigin:  snapshot.aggregate.AgentEndpoint.ClientOrigin.String(),
			ClientDialect: string(snapshot.aggregate.AgentEndpoint.ClientDialect),
		},
		Profiles:     make([]canonicalProfile, 0, len(snapshot.aggregate.Profiles)),
		Targets:      make([]canonicalTarget, 0, len(snapshot.compiledTargets)),
		Accounts:     make([]canonicalAccount, 0, len(snapshot.aggregate.AccountBindings)),
		RouteSets:    make([]canonicalRouteSet, 0, len(snapshot.aggregate.RouteSets)),
		Dependencies: make([]canonicalDependency, 0, len(snapshot.dependencies)),
		Egress: canonicalEgress{
			ID:       snapshot.aggregate.EgressPolicy.ID.String(),
			Revision: snapshot.aggregate.EgressPolicy.Revision,
			AccessID: snapshot.aggregate.EgressPolicy.AccessID.String(),
			Mode:     string(snapshot.aggregate.EgressPolicy.Mode),
		},
		Plugin: canonicalPlugin{
			Revision:   snapshot.pluginPlan.revision,
			AccessID:   snapshot.aggregate.PluginPlan.AccessID.String(),
			Mode:       string(snapshot.pluginPlan.mode),
			BindingIDs: pluginBindingIDStrings(snapshot.pluginPlan.bindingIDs),
		},
		Codec: canonicalCodec{
			ID:              snapshot.codecPlan.id.String(),
			Revision:        snapshot.codecPlan.revision,
			ClientDialect:   string(snapshot.codecPlan.clientDialect),
			ProviderDialect: string(snapshot.codecPlan.providerDialect),
			ClientOperations: make(
				[]canonicalClientOperation,
				0,
				len(snapshot.codecPlan.clientOperations),
			),
			RequiredCapabilities: slices.Clone(snapshot.codecPlan.requiredCapabilities),
		},
		Transport: canonicalTransportPlan{
			Requested: canonicalizeTransportTemplate(
				snapshot.transportPlan.requested,
			),
			Fallbacks: make(
				[]canonicalTransportTemplate,
				0,
				len(snapshot.transportPlan.fallbacks),
			),
		},
	}
	for _, operation := range snapshot.codecPlan.clientOperations {
		canonical.Codec.ClientOperations = append(
			canonical.Codec.ClientOperations,
			canonicalClientOperation{
				ID:             operation.id.String(),
				Revision:       operation.revision,
				ClientDialect:  string(operation.clientDialect),
				Methods:        slices.Clone(operation.methods),
				PathPattern:    operation.pathPattern,
				PathMatch:      string(operation.pathMatch),
				Kind:           string(operation.kind),
				Transport:      string(operation.transport),
				BodyKind:       string(operation.bodyKind),
				ReplayClass:    string(operation.replayClass),
				CodecFeature:   string(operation.codecFeature),
				MaxBodyBytes:   operation.maxBodyBytes,
				AllowedQueries: slices.Clone(operation.allowedQueries),
				EgressBearing:  operation.egressBearing,
			},
		)
	}
	for _, fallback := range snapshot.transportPlan.fallbacks {
		canonical.Transport.Fallbacks = append(
			canonical.Transport.Fallbacks,
			canonicalizeTransportTemplate(fallback),
		)
	}
	for _, profile := range snapshot.aggregate.Profiles {
		canonical.Profiles = append(canonical.Profiles, canonicalProfile{
			ID:                  profile.ID.String(),
			Revision:            profile.Revision,
			AccessID:            profile.AccessID.String(),
			Name:                profile.Name,
			Description:         profile.Description,
			BackendDialect:      string(profile.BackendDialect),
			TargetID:            profile.TargetID.String(),
			TransportProfileRef: profile.TransportProfileRef.String(),
			DefaultModelPolicy: canonicalModelPolicy{
				Revision:   profile.DefaultModelPolicy.Revision,
				Mode:       string(profile.DefaultModelPolicy.Mode),
				FixedModel: profile.DefaultModelPolicy.FixedModel.String(),
				MappingRef: profile.DefaultModelPolicy.MappingRef.String(),
			},
			AccountBindingIDs:       accountBindingIDStrings(profile.AccountBindingIDs),
			DefaultAccountBindingID: profile.DefaultAccountBindingID.String(),
		})
	}
	for _, target := range snapshot.compiledTargets {
		canonical.Targets = append(canonical.Targets, canonicalTarget{
			ID:            target.target.ID.String(),
			Revision:      target.target.Revision,
			AccessID:      target.target.AccessID.String(),
			ProfileID:     target.target.ProfileID.String(),
			Origin:        target.target.Origin.String(),
			BasePath:      target.basePath,
			HTTPAuthority: target.httpAuthority,
			TLSServerName: target.tlsServerName,
			Protocol:      string(target.target.Protocol),
			Capabilities:  slices.Clone(target.target.Capabilities),
		})
	}
	for _, account := range snapshot.aggregate.AccountBindings {
		canonical.Accounts = append(canonical.Accounts, canonicalAccount{
			ID:            account.ID.String(),
			Revision:      account.Revision,
			AccessID:      account.AccessID.String(),
			ProfileID:     account.ProfileID.String(),
			Label:         account.Label,
			SecretRef:     account.SecretRef.String(),
			AuthDriverRef: account.AuthDriverRef.String(),
			Enabled:       account.Enabled,
		})
	}
	for _, routeSet := range snapshot.aggregate.RouteSets {
		canonical.RouteSets = append(canonical.RouteSets, canonicalRouteSet{
			ID:                  routeSet.ID.String(),
			Revision:            routeSet.Revision,
			AccessID:            routeSet.AccessID.String(),
			CandidateProfileIDs: endpointProfileIDStrings(routeSet.CandidateProfileIDs),
		})
	}
	for _, dependency := range snapshot.dependencies {
		canonical.Dependencies = append(canonical.Dependencies, canonicalDependency{
			Kind:     string(dependency.Kind),
			ID:       dependency.ID,
			Revision: dependency.Revision,
		})
	}
	return json.Marshal(canonical)
}

func canonicalizeTransportTemplate(
	template TransportFingerprintTemplate,
) canonicalTransportTemplate {
	return canonicalTransportTemplate{
		Ref:           template.ref.String(),
		Revision:      template.revision,
		Source:        string(template.source),
		HTTPTransport: string(template.httpTransport),
		ALPN:          slices.Clone(template.alpn),
	}
}

func endpointProfileIDStrings(ids []EndpointProfileID) []string {
	output := make([]string, len(ids))
	for index, id := range ids {
		output[index] = id.String()
	}
	return output
}

func accountBindingIDStrings(ids []AccountBindingID) []string {
	output := make([]string, len(ids))
	for index, id := range ids {
		output[index] = id.String()
	}
	return output
}

func pluginBindingIDStrings(ids []PluginBindingID) []string {
	output := make([]string, len(ids))
	for index, id := range ids {
		output[index] = id.String()
	}
	return output
}
