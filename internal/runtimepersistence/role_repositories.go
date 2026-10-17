package runtimepersistence

import (
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func (s *ObservationStore) ActivityRepository() activity.Repository { return s.store.activityRepo }
func (s *ObservationStore) ConversationIdentityRepository() activity.ConversationIdentityRepository {
	return s.store.activityRepo
}
func (s *ObservationStore) ConversationProjectionWriter() activity.ConversationProjectionWriter {
	return s.store.activityRepo
}
func (s *ObservationStore) ExchangeContentRepository() exchangecontent.Repository {
	return s.store.exchangeContents
}
func (s *ControlStore) CaptureRunRepository() capturerun.Repository   { return s.store.captureRepo }
func (s *ControlStore) CodeLibraryRepository() codelibrary.Repository { return s.store.codeLibrary }
func (s *ControlStore) EgressProfileRepository() egressprofile.Repository {
	return s.store.egressProfiles
}
func (s *ControlStore) ManualCaptureRepository() manualcapture.Repository {
	return s.store.manualCapture
}
func (s *ControlStore) ProxyClientRepository() proxyclient.Repository { return s.store.proxyClients }
func (s *ObservationStore) EgressAttemptRepository() egressaudit.Repository {
	return s.store.egressAttempts
}
func (s *ObservationStore) ConnectionEventRepository() connectionevent.Repository {
	return s.store.connectionRepo
}
func (s *ControlStore) ToolApprovalRepository() toolapproval.Repository { return s.store.approvalRepo }
func (s *ControlStore) ConnectionRuleRepository() connectionpolicy.Repository {
	return s.store.connectionRule
}
func (s *ControlStore) EnvironmentRepository() environment.Repository { return s.store.environments }
func (s *ControlStore) CaptureAssignmentRepository() captureassignment.Repository {
	return s.store.captureAssignments
}
func (s *ControlStore) UpstreamEndpointRepository() upstreamendpoint.Repository {
	return s.store.upstreamEndpoints
}
func (s *ControlStore) ProviderAccountRepository() provideraccount.Repository {
	return s.store.providerAccounts
}
func (s *ObservationStore) RawEvidenceRepository() rawevidence.Repository { return s.store.rawEvidence }
func (s *ControlStore) RuntimeUserRepository() runtimeuser.Repository     { return s.store.runtimeUsers }
