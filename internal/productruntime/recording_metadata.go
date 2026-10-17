package productruntime

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"reflect"
	"strings"
	"time"
)

type recordedMetadata struct {
	usageBytes   uint64
	usageOutcome recording.Settlement
	usage        *runtimeusage.CollectedObservation
	usageSink    runtimeusage.CollectedWriter
	activity     activity.Record
	activitySink activity.PreparedRecorder
	identity     *agentconversation.ClientIdentity
	identitySink activity.ConversationIdentityRepository
}

func (o activityAttemptObserver) metadataReservation(stamp recording.Stamp, observation any) (*recording.Reservation, bool) {
	if !stamp.Current() {
		o.queue.budget.RecordOffer(recording.Activity, recording.DroppedStale, 0)
		return nil, false
	}
	n := snapshotBytes(reflect.ValueOf(observation))
	switch v := observation.(type) {
	case exchange.StartObservation:
		runtime, _ := v.Admission.RuntimeMetadata()
		n += snapshotBytes(reflect.ValueOf(runtime)) + uint64(len(v.Admission.SourceLabel()))
	case exchange.AttemptObservation:
		runtime, _ := v.Admission.RuntimeMetadata()
		n += snapshotBytes(reflect.ValueOf(runtime)) + uint64(len(v.Admission.SourceLabel()))
	}

	// Bounded admission attribution (32 KiB), prepared/projection overlap,
	// JSON escaping, encoder grow destination and SQL driver copies.
	n = 16*n + 128*1024
	if n > 64<<20 {
		o.queue.budget.RecordOffer(recording.Activity, recording.DroppedTooLarge, 0)
		return nil, false
	}
	r, ok := o.queue.budget.TryReserve(recording.Activity, int64(n))
	if !ok {
		o.queue.budget.RecordOffer(recording.Activity, recording.DroppedCapacity, 0)
	}
	return r, ok
}
func (o activityAttemptObserver) offerObserveStart(ctx context.Context, observation exchange.StartObservation) error {
	r, ok := o.metadataReservation(observation.ObservationStamp, observation)
	if !ok {
		return nil
	}
	source, err := exchangeActivitySource(observation.Admission, observation.HasAdmission)
	if err != nil {
		r.Release()
		return nil
	}
	conversation, err := observation.Conversation.Materialize()
	if err != nil {
		conversation, _ = agentconversation.Project(agentconversation.ProjectionInput{ExchangeID: observation.ExchangeID})
	}
	event := activity.Event{
		Kind:                   activity.KindExchangeStarted,
		EnvironmentID:          observation.EnvironmentID,
		EnvironmentRevision:    observation.EnvironmentRevision,
		EnvironmentDigest:      observation.EnvironmentDigest,
		ClientEndpointID:       observation.EndpointID,
		ClientEndpointRevision: observation.EndpointRevision,
		ProtocolPlanID:         observation.ProtocolPlanID,
		ProtocolPlanRevision:   observation.ProtocolPlanRevision,
		RouteID:                observation.RouteID,
		RouteRevision:          observation.RouteRevision,
		SubjectID:              observation.ExchangeID,
		Status:                 activity.StatusPending,
		SourceKind:             source.kind,
		SourceDisplayName:      source.displayName,
		SourceRecognition:      source.recognition,
		CaptureRunID:           source.captureRunID,
		ManualCaptureID:        source.manualCaptureID,
		ConnectionID:           observation.ConnectionID,
		Conversation:           conversation,
	}
	return o.offerPrepared(observation.ObservationStamp, r, event, observation.ClientProtocolEvidence, "", nil)
}
func (o activityAttemptObserver) offerObserveTerminal(ctx context.Context, observation exchange.AttemptObservation) error {
	r, ok := o.metadataReservation(observation.ObservationStamp, observation)
	if !ok {
		return nil
	}
	source, err := exchangeActivitySource(observation.Admission, observation.HasAdmission)
	if err != nil {
		r.Release()
		return nil
	}
	status := activity.StatusFailed
	switch observation.Outcome {
	case exchange.AttemptSucceeded:
		status = activity.StatusSucceeded
	case exchange.AttemptCanceled:
		status = activity.StatusCanceled
	case exchange.AttemptFailed, exchange.AttemptAborted:
	default:
		r.Release()
		return nil
	}
	conversation, err := observation.Conversation.Materialize()
	if err != nil {
		conversation, _ = agentconversation.Project(agentconversation.ProjectionInput{ExchangeID: observation.ExchangeID})
	}
	event := activity.Event{
		Kind:                   activity.KindExchangeCompleted,
		EnvironmentID:          observation.EnvironmentID,
		EnvironmentRevision:    observation.EnvironmentRevision,
		EnvironmentDigest:      observation.EnvironmentDigest,
		ClientEndpointID:       observation.EndpointID,
		ClientEndpointRevision: observation.EndpointRevision,
		ProtocolPlanID:         observation.ProtocolPlanID,
		ProtocolPlanRevision:   observation.ProtocolPlanRevision,
		RouteID:                observation.RouteID,
		RouteRevision:          observation.RouteRevision,
		AccountID:              observation.AccountID,
		AccountRevision:        observation.AccountRevision,
		CredentialEpoch:        observation.CredentialEpoch,
		SubjectID:              observation.ExchangeID,
		Status:                 status,
		ReasonCode:             string(observation.ReasonCode),
		SourceKind:             source.kind,
		SourceDisplayName:      source.displayName,
		SourceRecognition:      source.recognition,
		CaptureRunID:           source.captureRunID,
		ManualCaptureID:        source.manualCaptureID,
		ConnectionID:           observation.ConnectionID,
		Conversation:           conversation,
		Diagnosis: activity.Diagnosis{
			ProviderErrorCode: observation.ProviderErrorCode,
			ProviderStatus:    observation.ProviderStatus,
			ProviderField:     string(observation.ProviderField),
			ClientField:       string(observation.ClientField),
			ClientPath:        observation.ClientPath,
		},
		Transport: activityTransportEvidence(
			observation.Presentation,
			observation.Transport,
		),
	}
	return o.offerPrepared(observation.ObservationStamp, r, event, observation.ClientProtocolEvidence, observation.ProviderResponseID, o.collectUsage(observation, status))
}
func (o activityAttemptObserver) offerPrepared(stamp recording.Stamp, reservation *recording.Reservation, event activity.Event, evidence []protocolcore.ProtocolEvidenceValue, providerID string, usage *runtimeusage.CollectedObservation) error {
	sink, ok := o.recorder.(activity.PreparedRecorder)
	if !ok {
		reservation.Release()
		o.queue.budget.RecordOffer(recording.Activity, recording.DroppedUnavailable, 0)
		return nil
	}
	record, err := sink.Prepare(event)
	if err != nil {
		reservation.Release()
		o.queue.budget.RecordOffer(recording.Activity, recording.DroppedInvalid, 0)
		return nil
	}
	cloneSnapshot(reflect.ValueOf(&record).Elem())
	usageSink, _ := o.usage.(runtimeusage.CollectedWriter)
	bundle := &recordedMetadata{usage: usage, usageSink: usageSink, usageOutcome: recording.Discarded, activity: record, activitySink: sink, identitySink: o.identities}
	if identity, found := agentconversation.ClientIdentityFromProtocolEvidence(evidence, providerID, record.OccurredAt); found {
		cloneSnapshot(reflect.ValueOf(&identity).Elem())
		bundle.identity = &identity
	}
	bytes := snapshotBytes(reflect.ValueOf(record))
	if bundle.identity != nil {
		bytes += snapshotBytes(reflect.ValueOf(*bundle.identity))
	}
	if usage != nil {
		bundle.usageBytes = snapshotBytes(reflect.ValueOf(usage.Value))
		bytes += bundle.usageBytes
	}
	result := o.queue.enqueue(&recordingItem{bytes: int64(bytes), kind: recording.Activity, stamp: stamp, metadata: bundle, reservation: reservation})
	o.queue.budget.RecordOffer(recording.Activity, result, bytes)
	if usage != nil {
		o.queue.budget.RecordOffer(recording.Usage, result, bundle.usageBytes)
	}
	return nil
}
func (m *recordedMetadata) persist(ctx context.Context, report func(string, error)) error {
	_, activityErr := m.activitySink.RecordPrepared(ctx, m.activity)
	var identityErr error
	if m.identity != nil && m.identitySink != nil {
		identityErr = m.identitySink.PutConversationIdentity(ctx, m.activity.SubjectID, *m.identity)
	}
	var usageErr error
	if m.usage != nil && m.usageSink != nil {
		usageErr = m.usageSink.PutCollectedUsage(ctx, *m.usage)
		m.usageOutcome = recording.Persisted
		if usageErr != nil {
			m.usageOutcome = recording.Unconfirmed
			if errors.Is(usageErr, runtimeusage.ErrObservationDiscarded) {
				m.usageOutcome = recording.Discarded
			}
		}
	}
	if errors.Is(usageErr, runtimeusage.ErrObservationDiscarded) {
		usageErr = nil
	}
	if report != nil {
		if activityErr != nil {
			stage := "activity_terminal"
			if m.activity.Kind == activity.KindExchangeStarted {
				stage = "activity_start"
			}
			report(stage, activityErr)
		}
		if identityErr != nil {
			report("conversation_identity", identityErr)
		}
		if usageErr != nil {
			report("usage", usageErr)
		}
	}
	return errors.Join(activityErr, identityErr, usageErr)
}

// Nonallocating traversal of closed metadata values. Admission's receipt is an
// execution authority and is deliberately never traversed, copied or queued.
func snapshotBytes(v reflect.Value) uint64 {
	if v.Type() == reflect.TypeOf(captureadmission.Admission{}) {
		return 0
	}
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return uint64(v.Type().Size())
	}
	n := uint64(v.Type().Size())
	switch v.Kind() {
	case reflect.String:
		n += uint64(v.Len())
	case reflect.Pointer:
		if !v.IsNil() {
			n += snapshotBytes(v.Elem())
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			n += snapshotBytes(v.Index(i))
			if n > 64<<20 {
				return n
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			n += snapshotBytes(v.Field(i))
			if n > 64<<20 {
				return n
			}
		}
	}
	return n
}

// Only exported prepared projection values reach this ownership clone.
func cloneSnapshot(v reflect.Value) {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(v.Interface().(time.Time).UTC()))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(strings.Clone(v.String()))
	case reflect.Pointer:
		if !v.IsNil() {
			p := reflect.New(v.Type().Elem())
			p.Elem().Set(v.Elem())
			cloneSnapshot(p.Elem())
			v.Set(p)
		}
	case reflect.Slice:
		if !v.IsNil() {
			p := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			reflect.Copy(p, v)
			for i := 0; i < p.Len(); i++ {
				cloneSnapshot(p.Index(i))
			}
			v.Set(p)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			cloneSnapshot(v.Field(i))
		}
	}
}
