import 'dart:async';
import 'dart:collection';
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';

import '../../core/api/control_api.dart';
import '../../core/api/acp_models.dart';
import '../../core/api/launch_environment_snapshot.dart';
import '../../core/api/control_failure.dart';
import '../../core/api/account_facts_models.dart';
import '../../core/api/control_models.dart';
import '../../core/api/runtime_storage.dart';
import '../../core/bootstrap/root_trust_installer.dart';
import '../../core/bootstrap/public_certificate_exporter.dart';
import '../../core/bootstrap/runtime_connection.dart';
import '../../core/bootstrap/terminal_command.dart';
import '../../core/preferences/workbench_preferences.dart';
import 'runtime_connection_guide.dart';
import 'environment_editing.dart';

export '../../core/preferences/workbench_preferences.dart'
    show AppLanguage, WorkbenchSection, WorkbenchTheme;

enum RootCAGuideIntent { remove, replace }

final class WorkbenchController extends ChangeNotifier
    with WidgetsBindingObserver {
  Future<AccountResetRedemption> redeemAccountResetCredit(
    ProviderAccount account,
    AccountResetCredit credit,
  ) async {
    if (account.kind != 'codex_oauth' || !account.usable || !credit.available) {
      throw const ControlContractException('reset credit is unavailable');
    }
    return _api.redeemAccountResetCredit(account, credit.id);
  }

  Future<AccountFacts> accountFacts(
    ProviderAccount account, {
    bool history = false,
  }) async {
    final facts = await _api.accountFacts(account.id, history: history);
    if (facts.accountId != account.id ||
        facts.origin != account.credentialOrigin ||
        facts.credentialEpoch < account.credentialEpoch) {
      throw const ControlContractException(
        'account facts do not match the selected credential scope',
      );
    }
    return facts;
  }

  static const _exchangeDetailCacheLimit = 64;
  static const _fullExchangeDetailCacheLimit = 2;
  static const _exchangeDetailCacheByteLimit = 8 * 1024 * 1024;
  static const _captureConversationPageCacheLimit = 24;

  WorkbenchController({
    required ControlApi api,
    required TerminalCommandService terminalCommands,
    required this.previewMode,
    required Future<void> Function() closeRuntime,
    this.serverManagement = false,
    this.terminalManagement = true,
    this.rootTrustManagement = false,
    RootTrustInstaller? rootTrustInstaller,
    PublicCertificateExporter certificateExporter =
        const PlatformPublicCertificateExporter(),
    this.runtimeTarget = 'This Mac',
    Future<void> Function()? restartRuntime,
    this.chooseStorageDirectory,
    this.moveStorage,
    this.chooseStorageBackupDirectory,
    this.backupStorage,
    this.chooseStorageRestore,
    this.restoreStorage,
    this.storageMoveNotice,
    WorkbenchPreferences initialPreferences = const WorkbenchPreferences(),
    WorkbenchPreferencesStore preferencesStore =
        const DiscardWorkbenchPreferencesStore(),
    bool preferencesWritable = true,
    WorkbenchPreferencesIssue? initialPreferencesIssue,
    ValueChanged<WorkbenchTheme>? onThemeChanged,
    DateTime Function()? clock,
    this.webPrincipal,
    this.onSignOut,
    this.changeWebPassword,
  }) : _api = api,
       _terminalCommands = terminalCommands,
       _rootTrustInstaller = rootTrustInstaller,
       _certificateExporter = certificateExporter,
       _closeRuntime = closeRuntime,
       _restartRuntime = restartRuntime,
       _clock = clock ?? DateTime.now,
       _preferencesStore = preferencesStore,
       _preferencesWritable = preferencesWritable,
       _onThemeChanged = onThemeChanged,
       _desiredPreferences = initialPreferences,
       section =
           !serverManagement &&
               initialPreferences.section == WorkbenchSection.usage
           ? WorkbenchSection.captures
           : initialPreferences.section,
       language = initialPreferences.language,
       theme = initialPreferences.theme,
       selectedCaptureKey = initialPreferences.selectedCaptureKey,
       selectedEnvironmentId = initialPreferences.selectedEnvironmentId,
       selectedEnvironmentRevision =
           initialPreferences.selectedEnvironmentRevision,
       selectedEndpointId = initialPreferences.selectedEndpointId,
       preferenceWarning = initialPreferencesIssue?.copyKey;

  final ControlApi _api;

  Future<List<LaunchEnvironmentSnapshot>> launchEnvironmentSnapshots() =>
      _api.launchEnvironmentSnapshots();
  final TerminalCommandService _terminalCommands;
  final RootTrustInstaller? _rootTrustInstaller;
  final PublicCertificateExporter _certificateExporter;
  final Future<void> Function() _closeRuntime;
  final Future<void> Function()? _restartRuntime;
  final Future<String?> Function()? chooseStorageDirectory;
  final Future<void> Function(String target)? moveStorage;
  final Future<String?> Function()? chooseStorageBackupDirectory;
  final Future<void> Function(String target)? backupStorage;
  final Future<StorageRestoreSelection?> Function()? chooseStorageRestore;
  final Future<void> Function(StorageRestoreSelection selection)?
  restoreStorage;
  String? storageMoveNotice;
  String? storageMoveFailure;
  bool storageMoving = false;
  final DateTime Function() _clock;
  final WorkbenchPreferencesStore _preferencesStore;
  final bool _preferencesWritable;
  final ValueChanged<WorkbenchTheme>? _onThemeChanged;
  final bool previewMode;
  final bool serverManagement;
  final bool terminalManagement;
  final bool rootTrustManagement;
  final String runtimeTarget;
  final RuntimeWebPrincipal? webPrincipal;
  final Future<void> Function()? onSignOut;
  final Future<void> Function(String currentPassword, String newPassword)?
  changeWebPassword;

  RuntimeConnectionGuide get connectionGuide => RuntimeConnectionGuide(
    connectedTarget: runtimeTarget,
    advertised: serverAccess,
  );

  String get runtimeConnectTarget =>
      connectionGuide.address?.authority ?? runtimeTarget;
  String get runtimeServerURL => connectionGuide.serverURL ?? '';
  String get runtimeWebURL => connectionGuide.webURL ?? '';

  bool get accessSettingsAvailable =>
      serverManagement || terminalManagement || webPrincipal != null;

  List<SettingsDestination> get settingsDestinations => [
    SettingsDestination.preferences,
    if (accessSettingsAvailable) SettingsDestination.access,
    if (serverManagement) SettingsDestination.users,
    SettingsDestination.safety,
    SettingsDestination.networkExits,
  ];

  bool remoteConnectionGuideRequested = false;

  Future<RuntimeRootCertificate> loadRuntimeRootCA() => _api.runtimeRootCA();

  Future<bool> saveRuntimeRootCA(RuntimeRootCertificate certificate) =>
      _certificateExporter.save(certificate);

  DashboardData? data;
  NetworkData? networkData;
  List<ApprovalRecord>? pendingApprovals;
  ConversationPage? selectedCaptureConversations;
  ActivityPage? selectedCapturePage;
  EnvironmentDraft? reviewedEnvironmentDraft;
  EnvironmentImpact? reviewedEnvironmentImpact;
  EnvironmentRecord? historicalEnvironment;
  CaptureAssignment? selectedAssignment;
  bool selectedCaptureLaunchIncomplete = false;
  ACPRecord? selectedACP;
  TerminalCommandStatus? terminalCommand;
  RuntimeServerAccess? serverAccess;
  List<RuntimeUser>? runtimeUsers;
  RuntimeUsageReport? runtimeUsage;
  RootCAStatus? rootCAStatus;
  RuntimeStorageLocation? storageLocation;
  RuntimeStorageLocation? previousStorageLocation;
  bool storageLocationLoading = false;
  bool storageLocationFailed = false;
  bool storageCleanupRunning = false;
  bool storageArchivePreviewLoading = false;
  String? storageCleanupNotice;
  String? storageCleanupError;
  String? storageArchivePreviewError;
  RootCAGuideIntent? rootCAGuideIntent;
  CapturedMessageTransformSample? capturedMessageTransformSample;
  final int usageRangeDays = 365;
  WorkbenchSection section;
  AppLanguage language;
  WorkbenchTheme theme;
  String? selectedCaptureKey;
  String? selectedCaptureConversationKey;
  String? selectedEnvironmentId;
  int? selectedEnvironmentRevision;
  String? selectedEndpointId;
  String? preferenceWarning;
  String? errorMessage;
  String? operationNotice;
  String? networkError;
  String? networkErrorDiagnostic;
  String? captureDirectoryError;
  String? networkNotice;
  String? inventoryError;
  String? inventoryErrorDiagnostic;
  String? inventoryNotice;
  String? environmentError;
  String? environmentErrorDiagnostic;
  String? environmentNotice;
  String? terminalCommandError;
  String? terminalCommandNotice;
  String? serverManagementError;
  String? rootCAError;
  String? approvalAttentionError;
  bool loading = true;
  bool detailLoading = false;
  bool mutating = false;
  bool networkLoading = false;
  bool captureDirectoryLoading = false;
  bool captureActivitiesLoading = false;
  bool networkMutating = false;
  bool inventoryMutating = false;
  bool environmentMutating = false;
  bool environmentRevisionLoading = false;
  bool terminalCommandLoading = false;
  bool terminalCommandMutating = false;
  bool serverManagementLoading = false;
  bool runtimeUserMutating = false;
  bool rootCALoading = false;
  bool rootCAMutating = false;
  bool rootCARestartRequired = false;
  bool pendingApprovalsLoading = false;
  int _dashboardGeneration = 0;
  int _selectionGeneration = 0;
  int _environmentRevisionGeneration = 0;
  final LinkedHashMap<String, ExchangeDetail> _exchangeDetails =
      LinkedHashMap<String, ExchangeDetail>();
  final LinkedHashMap<String, ActivityPage> _captureConversationPages =
      LinkedHashMap<String, ActivityPage>();
  final Map<String, Future<ExchangeDetail?>> _exchangeLoads = {};
  final Map<String, int> _exchangeLoadGenerations = {};
  final Map<String, String> _exchangeErrors = {};
  int _exchangeLoadGeneration = 0;
  final LinkedHashMap<String, RawEvidencePage> _rawEvidencePages =
      LinkedHashMap<String, RawEvidencePage>();
  final Set<String> _loadingRawEvidence = {};
  final Map<String, String> _rawEvidenceErrors = {};
  final Map<String, UpstreamModelCatalog> _upstreamModelCatalogs = {};
  final Map<String, ClientModelCatalog> _clientModelCatalogs = {};
  Future<CodeLibraryCatalog>? _codeLibraryCatalog;
  int _rawEvidenceGeneration = 0;

  int settingsTab = 0;
  Timer? _poller;
  Timer? _evidencePoller;
  bool _pollInFlight = false;
  bool _evidencePollInFlight = false;
  bool _pollingVisible = true;
  bool _observingLifecycle = false;
  int _captureDetailLoads = 0;
  bool _disposed = false;
  WorkbenchPreferences? _desiredPreferences;
  WorkbenchPreferences? _pendingPreferences;
  Future<void> _preferenceDrain = Future<void>.value();
  bool _preferenceDraining = false;

  List<CaptureRecord> get runningCaptures {
    final values =
        data?.captures.where((capture) => capture.running).toList() ?? [];
    values.sort((left, right) => right.updatedAt.compareTo(left.updatedAt));
    return values;
  }

  List<CaptureRecord> get historicalCaptures {
    final values =
        data?.captures.where((capture) => !capture.running).toList() ?? [];
    values.sort((left, right) => right.updatedAt.compareTo(left.updatedAt));
    return values;
  }

  CaptureRecord? get selectedCapture {
    final key = selectedCaptureKey;
    if (key == null) return null;
    return data?.captures.where((capture) => capture.key == key).firstOrNull;
  }

  EnvironmentRecord? get selectedEnvironment {
    final id = selectedEnvironmentId;
    if (id == null) return null;
    return data?.environments
        .where((environment) => environment.id == id)
        .firstOrNull;
  }

  EnvironmentRecord? get displayedEnvironment =>
      historicalEnvironment ?? selectedEnvironment;

  bool get inspectingHistoricalEnvironment =>
      selectedEnvironmentRevision != null;

  UpstreamEndpoint? get selectedEndpoint {
    final id = selectedEndpointId;
    if (id == null) return null;
    return data?.endpoints.where((endpoint) => endpoint.id == id).firstOrNull;
  }

  String _upstreamModelCatalogKey(String endpointId, String accountId) =>
      '$endpointId\u0000$accountId';

  UpstreamModelCatalog? upstreamModelCatalog(
    String endpointId,
    String accountId,
  ) => _upstreamModelCatalogs[_upstreamModelCatalogKey(endpointId, accountId)];

  Future<UpstreamModelCatalog> upstreamModels(
    String endpointId, {
    required String accountId,
    bool refresh = false,
  }) async {
    final key = _upstreamModelCatalogKey(endpointId, accountId);
    // The server cache is TTL-, endpoint-, account-revision- and credential-
    // epoch-bound. Keep a last-known snapshot for display while loading, but
    // never let an unbounded UI cache bypass those availability checks.
    final catalog = await _api.upstreamModels(
      endpointId,
      accountId: accountId,
      refresh: refresh,
    );
    if (!_disposed) {
      _upstreamModelCatalogs[key] = catalog;
      notifyListeners();
    }
    return catalog;
  }

  ClientModelCatalog? clientModelCatalog(String protocol) =>
      _clientModelCatalogs[protocol];

  Future<ClientModelCatalog> clientModels(
    String protocol, {
    bool refresh = false,
  }) async {
    if (!refresh) {
      final cached = _clientModelCatalogs[protocol];
      if (cached != null) return cached;
    }
    final catalog = await _api.clientModels(protocol);
    if (!_disposed) {
      _clientModelCatalogs[protocol] = catalog;
      notifyListeners();
    }
    return catalog;
  }

  Future<MessageTransformTestResult> testMessageTransform({
    required String wireProtocol,
    required TrafficTransformPolicy policy,
    MessageTransformTestSample? sample,
  }) => _api.testMessageTransform(
    wireProtocol: wireProtocol,
    policy: policy,
    sample: sample,
  );

  Future<AccountSelectorTestResult> testAccountSelector({
    required AccountSelectorPolicy policy,
    required AccountSelectorTestSample sample,
  }) => _api.testAccountSelector(policy: policy, sample: sample);

  Future<EnvironmentDryRun> dryRunEnvironment(EnvironmentDryRunInput input) =>
      _api.dryRunEnvironment(input);

  Future<EvidenceSearchPage> searchEvidence(EvidenceSearchRequest request) =>
      _api.searchEvidence(request);

  Future<EnvironmentDraft> environmentDraft(String environmentId) =>
      _api.environmentDraft(environmentId);

  Future<CodeLibraryCatalog> codeLibrary({bool refresh = false}) {
    if (refresh) _codeLibraryCatalog = null;
    return _codeLibraryCatalog ??= _loadCodeLibrary();
  }

  Future<CodeLibraryCatalog> _loadCodeLibrary() async {
    try {
      return await _api.codeLibrary();
    } on Object {
      _codeLibraryCatalog = null;
      rethrow;
    }
  }

  Future<T> _codeLibraryMutation<T>(Future<T> mutation) async {
    final result = await mutation;
    _codeLibraryCatalog = null;
    return result;
  }

  Future<CodeLibraryCollection> createCodeLibraryCollection({
    required String displayName,
  }) => _codeLibraryMutation(
    _api.createCodeLibraryCollection(
      id: 'collection.custom.${_newUuid()}',
      displayName: displayName,
    ),
  );

  Future<CodeLibraryTransformRevision> createCodeLibraryTransform({
    required String collectionId,
    required String displayName,
    required TrafficTransformPolicy policy,
  }) => _codeLibraryMutation(
    _api.publishCodeLibraryTransform(
      id: 'transform.custom.${_newUuid()}',
      expectedRevision: 0,
      collectionId: collectionId,
      displayName: displayName,
      policy: policy,
    ),
  );

  Future<CodeLibraryTransformRevision> publishCodeLibraryTransform({
    required String id,
    required int expectedRevision,
    required String collectionId,
    required String displayName,
    required TrafficTransformPolicy policy,
  }) => _codeLibraryMutation(
    _api.publishCodeLibraryTransform(
      id: id,
      expectedRevision: expectedRevision,
      collectionId: collectionId,
      displayName: displayName,
      policy: policy,
    ),
  );

  Future<CodeLibraryAccountSelectorRevision> createCodeLibraryAccountSelector({
    required String collectionId,
    required String displayName,
    required AccountSelectorPolicy policy,
  }) => _codeLibraryMutation(
    _api.publishCodeLibraryAccountSelector(
      id: 'selector.custom.${_newUuid()}',
      expectedRevision: 0,
      collectionId: collectionId,
      displayName: displayName,
      policy: policy,
    ),
  );

  Future<CodeLibraryAccountSelectorRevision> publishCodeLibraryAccountSelector({
    required String id,
    required int expectedRevision,
    required String collectionId,
    required String displayName,
    required AccountSelectorPolicy policy,
  }) => _codeLibraryMutation(
    _api.publishCodeLibraryAccountSelector(
      id: id,
      expectedRevision: expectedRevision,
      collectionId: collectionId,
      displayName: displayName,
      policy: policy,
    ),
  );

  Future<EgressProfileCatalog> egressProfiles() => _api.egressProfiles();

  Future<EgressProfileRevision> createEgressProfile({
    required String displayName,
    required TrafficEgressPolicy policy,
  }) => _api.publishEgressProfile(
    id: 'profile.custom.${_newUuid()}',
    expectedRevision: 0,
    displayName: displayName,
    policy: policy,
  );

  Future<EgressProfileRevision> publishEgressProfile({
    required String id,
    required int expectedRevision,
    required String displayName,
    required TrafficEgressPolicy policy,
  }) => _api.publishEgressProfile(
    id: id,
    expectedRevision: expectedRevision,
    displayName: displayName,
    policy: policy,
  );

  int? get pendingApprovalCount => pendingApprovals?.length;

  List<ActivityRecord> get selectedActivities =>
      selectedCapturePage?.items ?? const [];

  List<ConversationSummary> get captureConversations =>
      selectedCaptureConversations?.items
          .map(ConversationSummary.fromRecord)
          .toList(growable: false) ??
      const [];

  ConversationSummary? get selectedCaptureConversation {
    final key = selectedCaptureConversationKey;
    if (key == null) return null;
    return captureConversations.where((value) => value.key == key).firstOrNull;
  }

  ExchangeDetail? exchangeDetail(
    String exchangeId, {
    String contentView = 'incremental',
  }) {
    final key = '$exchangeId:$contentView';
    final cached = _exchangeDetails.remove(key);
    if (cached != null) _exchangeDetails[key] = cached;
    return cached;
  }

  bool exchangeIsLoading(
    String exchangeId, {
    String contentView = 'incremental',
  }) => _exchangeLoads.containsKey('$exchangeId:$contentView');

  String? exchangeError(
    String exchangeId, {
    String contentView = 'incremental',
  }) => _exchangeErrors['$exchangeId:$contentView'];

  RawEvidencePage? rawEvidence(String exchangeId) =>
      _rawEvidencePages[exchangeId];

  bool rawEvidenceIsLoading(String exchangeId) =>
      _loadingRawEvidence.contains(exchangeId);

  String? rawEvidenceError(String exchangeId) => _rawEvidenceErrors[exchangeId];

  String activityAccountLabel(ActivityRecord activity) {
    final id = activity.accountId;
    if (id == null) return '';
    return data?.accounts
            .where((account) => account.id == id)
            .firstOrNull
            ?.displayName ??
        id;
  }

  String activityEndpointLabel(ActivityRecord activity) {
    final environment = data?.environments
        .where((value) => value.id == activity.environmentId)
        .firstOrNull;
    final route = environment?.routes
        .where((value) => value.id == activity.routeId)
        .firstOrNull;
    final endpointId = route?.endpointId;
    if (endpointId == null) return '';
    return data?.endpoints
            .where((endpoint) => endpoint.id == endpointId)
            .firstOrNull
            ?.displayName ??
        endpointId;
  }

  Future<void> initialize() async {
    if (!_observingLifecycle) {
      WidgetsFlutterBinding.ensureInitialized().addObserver(this);
      _observingLifecycle = true;
    }
    await refresh();
    if (_disposed) return;
    if (terminalManagement) await _maintainTerminalCommand();
    if (_disposed) return;
    final environmentId = selectedEnvironmentId;
    final revision = selectedEnvironmentRevision;
    if (environmentId != null && revision != null) {
      await inspectEnvironmentRevision(
        environmentId,
        revision,
        navigate: false,
      );
    }
    if (_disposed) return;
    _poller = Timer.periodic(
      const Duration(seconds: 5),
      (_) => unawaited(_poll()),
    );
    // Follow the visible live conversation independently of slow inventory
    // reads. This bounded point query never reloads accounts or credentials.
    _evidencePoller = Timer.periodic(
      const Duration(seconds: 1),
      (_) => unawaited(_pollSelectedEvidence()),
    );
    if (storageMoveNotice != null) {
      selectSection(WorkbenchSection.settings);
      selectSettingsTab(
        settingsDestinations.indexOf(SettingsDestination.safety),
      );
    }
  }

  Future<void> refreshRootCA({bool quiet = false}) async {
    if (_disposed || !rootTrustManagement || rootCALoading || rootCAMutating) {
      return;
    }
    if (!quiet) {
      rootCALoading = true;
      rootCAError = null;
      notifyListeners();
    }
    try {
      final status = await _api.rootCA();
      if (_disposed) return;
      rootCAStatus = status;
      final guideIntent = rootCAGuideIntent;
      if (guideIntent != null && status.certificatePresent == 'absent') {
        rootCAGuideIntent = null;
      }
      rootCAError = null;
      rootCALoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || quiet) return;
      rootCALoading = false;
      rootCAError = _describeError(error);
      notifyListeners();
    }
  }

  Future<bool> installAndTrustRootCA() => _installAndTrustRootCA();

  Future<bool> openRootCARemovalGuide() =>
      _showRootCAGuide(RootCAGuideIntent.remove);

  Future<bool> replaceRootCA() async {
    final current = rootCAStatus;
    if (current != null && current.certificatePresent != 'absent') {
      return _showRootCAGuide(RootCAGuideIntent.replace);
    }
    final accepted = await _applyRootCAAction(
      (status) => _api.replaceRootCA(status),
    );
    if (accepted && rootCARestartRequired && _restartRuntime != null) {
      await restartForRootReset();
    }
    return accepted;
  }

  Future<bool> _showRootCAGuide(RootCAGuideIntent intent) async {
    if (_disposed ||
        rootCAStatus == null ||
        rootCAMutating ||
        rootCARestartRequired) {
      return false;
    }
    rootCAError = null;
    operationNotice = null;
    rootCAGuideIntent = intent;
    notifyListeners();
    return true;
  }

  Future<bool> _installAndTrustRootCA() async {
    final current = rootCAStatus;
    final installer = _rootTrustInstaller;
    if (_disposed ||
        current == null ||
        installer == null ||
        rootCAMutating ||
        rootCARestartRequired) {
      return false;
    }
    rootCAMutating = true;
    rootCAError = null;
    operationNotice = null;
    notifyListeners();
    try {
      final material = await _api.rootCAMaterial(current);
      await installer.install(material);
      if (_disposed) return false;
      final observed = await _api.rootCA();
      if (_disposed) return false;
      rootCAStatus = observed;
      rootCAMutating = false;
      if (observed.rootRevision != current.rootRevision ||
          observed.fingerprint != current.fingerprint ||
          !observed.installed) {
        rootCAError = 'root_trust_install_not_confirmed';
        notifyListeners();
        return false;
      }
      rootCAGuideIntent = null;
      operationNotice = 'root_ca_updated';
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      try {
        rootCAStatus = await _api.rootCA();
      } on Object {
        // Preserve the installation failure as the actionable primary error.
      }
      if (_disposed) return false;
      rootCAMutating = false;
      rootCAError = switch (error) {
        RootTrustInstallerException exception =>
          'root_trust_install_${exception.failure.name}',
        _ => _describeError(error),
      };
      notifyListeners();
      return false;
    }
  }

  Future<void> restartForRootReset() async {
    final restart = _restartRuntime;
    if (_disposed || restart == null) return;
    await restart();
  }

  Future<bool> _applyRootCAAction(
    Future<RootCAActionResult> Function(RootCAStatus current) call,
  ) async {
    final current = rootCAStatus;
    if (_disposed ||
        current == null ||
        rootCAMutating ||
        rootCARestartRequired) {
      return false;
    }
    rootCAMutating = true;
    rootCAError = null;
    operationNotice = null;
    notifyListeners();
    try {
      final result = await call(current);
      if (_disposed) return false;
      rootCAStatus = result.status;
      rootCAMutating = false;
      rootCARestartRequired = result.restartRequired;
      final accepted = result.completed || result.restartRequired;
      operationNotice = accepted
          ? result.restartRequired
                ? 'root_ca_restart_required'
                : 'root_ca_updated'
          : null;
      notifyListeners();
      return accepted;
    } catch (error) {
      if (_disposed) return false;
      rootCAMutating = false;
      rootCAError = _describeError(error);
      notifyListeners();
      return false;
    }
  }

  Future<void> refresh({bool selectDefaults = false}) async {
    if (_disposed || inventoryMutating || environmentMutating) return;
    final generation = ++_dashboardGeneration;
    if (data == null) loading = true;
    errorMessage = null;
    notifyListeners();
    try {
      final updated = await _api.loadDashboard();
      if (_disposed || generation != _dashboardGeneration) return;
      data = updated;
      _repairDashboardSelections(updated, forceCaptureDefault: selectDefaults);
      loading = false;
      notifyListeners();
      if (selectedCapture != null) await _loadCaptureDetail(selectedCapture!);
      if (section == WorkbenchSection.network) {
        await _refreshNetwork();
      } else {
        await refreshPendingApprovals();
      }
      if (section == WorkbenchSection.settings ||
          section == WorkbenchSection.usage) {
        if (serverManagement) await refreshServerManagement();
        if (section == WorkbenchSection.settings && terminalManagement) {
          await refreshTerminalCommand();
        }
        if (section == WorkbenchSection.settings && rootTrustManagement) {
          await refreshRootCA();
        }
      }
    } catch (error) {
      if (_disposed || generation != _dashboardGeneration) return;
      loading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> _poll() async {
    if (_disposed ||
        !_pollingVisible ||
        _pollInFlight ||
        loading ||
        captureDirectoryLoading ||
        mutating ||
        networkMutating ||
        inventoryMutating ||
        environmentMutating) {
      return;
    }
    _pollInFlight = true;
    final generation = _dashboardGeneration;
    try {
      final updated = await _api.loadDashboard();
      if (_disposed ||
          generation != _dashboardGeneration ||
          inventoryMutating) {
        return;
      }
      final previous = data;
      data = _mergePolledDashboard(previous, updated);
      _repairDashboardSelections(data!);
      if (!_sameDashboard(previous, data!)) notifyListeners();
      if (section != WorkbenchSection.network) {
        await refreshPendingApprovals(quiet: true);
      }
      final capture = selectedCapture;
      if (section == WorkbenchSection.captures &&
          capture != null &&
          !_evidencePollInFlight &&
          !capture.running &&
          !selectedActivities.any((item) => item.status == 'pending')) {
        await _loadCaptureDetail(capture, quiet: true);
      }
      if (section == WorkbenchSection.network) {
        await _refreshNetwork(quiet: true);
      }
      if (section == WorkbenchSection.settings ||
          section == WorkbenchSection.usage) {
        if (serverManagement) await refreshServerManagement(quiet: true);
        if (section == WorkbenchSection.settings && terminalManagement) {
          await refreshTerminalCommand(quiet: true);
        }
      }
    } catch (_) {
      // A transient poll must not replace useful evidence with an error page.
      // Explicit refresh still surfaces the exact failure.
    } finally {
      _pollInFlight = false;
    }
  }

  Future<void> _pollSelectedEvidence() async {
    final capture = selectedCapture;
    if (_disposed ||
        !_pollingVisible ||
        section != WorkbenchSection.captures ||
        capture == null ||
        _evidencePollInFlight ||
        _captureDetailLoads != 0 ||
        loading ||
        detailLoading ||
        captureActivitiesLoading ||
        inventoryMutating ||
        environmentMutating ||
        (!capture.running &&
            !selectedActivities.any((item) => item.status == 'pending'))) {
      return;
    }
    _evidencePollInFlight = true;
    final generation = _selectionGeneration;
    final conversationKey = selectedCaptureConversationKey;
    try {
      if (conversationKey == null || capture.isManual) {
        // A manual Capture can receive independent Exchanges after its first.
        // Probe the newest Activity without reloading its directory each tick.
        final latest = await _captureActivityPage(capture, limit: 1);
        if (_disposed ||
            generation != _selectionGeneration ||
            capture.key != selectedCaptureKey ||
            conversationKey != selectedCaptureConversationKey) {
          return;
        }
        final newest = captureConversations.firstOrNull;
        final observed = latest.items.firstOrNull;
        if (observed != null &&
            (newest == null ||
                observed.id != newest.latest.id ||
                observed.occurredAt != newest.latest.occurredAt ||
                observed.reasonCode != newest.latest.reasonCode ||
                observed.status != newest.latest.status)) {
          await _loadCaptureDetail(
            capture,
            quiet: true,
            followLatest: capture.isManual && conversationKey == newest?.key,
          );
          return;
        }
        if (conversationKey == null ||
            !selectedActivities.any((item) => item.status == 'pending')) {
          return;
        }
      }
      final latest = await _captureActivityPage(
        capture,
        conversationId: conversationKey,
        limit: 100,
      );
      if (_disposed ||
          generation != _selectionGeneration ||
          capture.key != selectedCaptureKey ||
          conversationKey != selectedCaptureConversationKey) {
        return;
      }
      if (latest.items.isEmpty) {
        // A pending Exchange can acquire its native Conversation on completion.
        // Resolve that identity before replacing a useful visible timeline.
        await _loadCaptureDetail(capture, quiet: true);
        return;
      }
      final current = selectedCapturePage;
      if (current != null &&
          current.nextCursor == latest.nextCursor &&
          current.items.length == latest.items.length &&
          current.items.indexed.every((entry) {
            final other = latest.items[entry.$1];
            return entry.$2.id == other.id &&
                entry.$2.occurredAt == other.occurredAt &&
                entry.$2.reasonCode == other.reasonCode &&
                entry.$2.status == other.status;
          })) {
        return;
      }
      selectedCapturePage = _reconcileCaptureConversationPage(current, latest);
      _cacheCaptureConversationPage(
        capture.key,
        conversationKey,
        selectedCapturePage!,
      );
      notifyListeners();
    } catch (_) {
      // Background failure retains readable evidence. Explicit refresh exposes
      // errors, and the ordinary directory poll repairs changed identities.
    } finally {
      _evidencePollInFlight = false;
    }
  }

  Future<void> loadMoreCaptures() async {
    final current = data;
    final cursor = current?.captureNextCursor;
    if (_disposed ||
        loading ||
        inventoryMutating ||
        environmentMutating ||
        current == null ||
        cursor == null ||
        captureDirectoryLoading) {
      return;
    }
    captureDirectoryLoading = true;
    captureDirectoryError = null;
    final generation = _dashboardGeneration;
    notifyListeners();
    try {
      final page = await _api.captures(cursor: cursor);
      if (_disposed ||
          generation != _dashboardGeneration ||
          inventoryMutating) {
        if (!_disposed) {
          captureDirectoryLoading = false;
          notifyListeners();
        }
        return;
      }
      final merged = <String, CaptureRecord>{
        for (final capture in current.captures) capture.key: capture,
        for (final capture in page.items) capture.key: capture,
      };
      data = _dashboardWith(
        current,
        captures: merged.values.toList(growable: false),
        captureNextCursor: page.nextCursor,
        replaceCaptureCursor: true,
      );
      captureDirectoryLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed) return;
      captureDirectoryLoading = false;
      captureDirectoryError = _describeError(error);
      notifyListeners();
    }
  }

  void selectSection(WorkbenchSection value) {
    if (value == WorkbenchSection.usage && !serverManagement) return;
    if (section == value) return;
    section = value;
    operationNotice = null;
    notifyListeners();
    if (value == WorkbenchSection.network && networkData == null) {
      unawaited(_refreshNetwork());
    }
    if (value == WorkbenchSection.settings || value == WorkbenchSection.usage) {
      if (serverManagement &&
          (serverAccess == null ||
              runtimeUsers == null ||
              value == WorkbenchSection.usage && runtimeUsage == null)) {
        unawaited(refreshServerManagement());
      }
      if (terminalManagement && terminalCommand == null) {
        unawaited(refreshTerminalCommand());
      }
      if (value == WorkbenchSection.settings &&
          rootTrustManagement &&
          rootCAStatus == null) {
        unawaited(refreshRootCA());
      }
    }
  }

  void selectSettingsTab(int value) {
    if (value < 0 ||
        value >= settingsDestinations.length ||
        settingsTab == value) {
      return;
    }
    settingsTab = value;
    notifyListeners();
    if (settingsDestinations[value] == SettingsDestination.safety &&
        storageLocation == null) {
      unawaited(refreshStorageLocation());
    }
  }

  Future<void> refreshStorageLocation() async {
    if (_disposed || storageLocationLoading) return;
    storageLocationLoading = true;
    storageLocationFailed = false;
    notifyListeners();
    try {
      final location = await _api.storageLocation();
      if (_disposed) return;
      previousStorageLocation = storageLocation;
      storageLocation = location;
    } catch (_) {
      if (_disposed) return;
      storageLocationFailed = true;
    } finally {
      if (!_disposed) {
        storageLocationLoading = false;
        notifyListeners();
      }
    }
  }

  Future<DeletionOutcome> cleanupExpiredEvidence() async {
    if (_disposed || storageCleanupRunning) {
      throw const ControlContractException('storage cleanup is unavailable');
    }
    storageCleanupRunning = true;
    storageCleanupNotice = null;
    storageCleanupError = null;
    notifyListeners();
    try {
      final outcome = await _api.cleanupExpiredEvidence();
      if (_disposed) {
        throw const ControlContractException('storage cleanup is unavailable');
      }
      storageCleanupNotice = 'settings.storage.cleanup_complete';
      await refreshStorageLocation();
      return outcome;
    } catch (error) {
      if (!_disposed) storageCleanupError = _describeError(error);
      rethrow;
    } finally {
      if (!_disposed) {
        storageCleanupRunning = false;
        notifyListeners();
      }
    }
  }

  Future<DeletionReleased?> loadEvidenceClearPreview() async {
    if (_disposed || storageArchivePreviewLoading) return null;
    storageArchivePreviewLoading = true;
    storageArchivePreviewError = null;
    notifyListeners();
    try {
      final preview = await _api.evidenceClearPreview();
      if (_disposed) return null;
      return preview;
    } catch (error) {
      if (!_disposed) storageArchivePreviewError = _describeError(error);
      return null;
    } finally {
      if (!_disposed) {
        storageArchivePreviewLoading = false;
        notifyListeners();
      }
    }
  }

  Future<void> relocateStorage(String target) async {
    final action = moveStorage;
    if (action == null) return;
    await _runStorageAction(() => action(target));
  }

  Future<void> createStorageBackup(String target) async {
    final action = backupStorage;
    if (action == null) return;
    await _runStorageAction(() => action(target));
  }

  Future<void> restoreStorageBackup(StorageRestoreSelection selection) async {
    final action = restoreStorage;
    if (action == null) return;
    await _runStorageAction(() => action(selection));
  }

  Future<void> _runStorageAction(Future<void> Function() action) async {
    if (_disposed || storageMoving) return;
    storageMoving = true;
    storageMoveFailure = null;
    storageMoveNotice = null;
    notifyListeners();
    try {
      await action();
    } catch (error) {
      if (_disposed) return;
      storageMoveFailure = storageMoveErrorKey(error);
    } finally {
      if (!_disposed) {
        storageMoving = false;
        notifyListeners();
      }
    }
  }

  Future<String?> pickStorageDirectory() async {
    return _pickStoragePath(chooseStorageDirectory);
  }

  Future<String?> pickStorageBackupDirectory() async {
    return _pickStoragePath(chooseStorageBackupDirectory);
  }

  Future<StorageRestoreSelection?> pickStorageRestore() async {
    if (_disposed || storageMoving || chooseStorageRestore == null) return null;
    try {
      return await chooseStorageRestore!();
    } catch (_) {
      if (!_disposed) {
        storageMoveFailure = 'settings.storage.picker_failed';
        notifyListeners();
      }
      return null;
    }
  }

  Future<String?> _pickStoragePath(Future<String?> Function()? picker) async {
    if (_disposed || storageMoving || picker == null) return null;
    try {
      return await picker();
    } catch (_) {
      if (!_disposed) {
        storageMoveFailure = 'settings.storage.picker_failed';
        notifyListeners();
      }
      return null;
    }
  }

  static String storageMoveErrorKey(Object error) {
    final code = error.toString();
    return 'settings.storage.${const {'storage_target_invalid', 'storage_in_use', 'storage_copy_failed', 'storage_validation_failed', 'storage_settings_invalid', 'backup_validation_failed', 'backup_incompatible'}.contains(code) ? code : 'storage_copy_failed'}';
  }

  void openRuntimeUsersSettings() {
    if (!serverManagement) return;
    settingsTab = settingsDestinations.indexOf(SettingsDestination.users);
    section = WorkbenchSection.settings;
    operationNotice = null;
    notifyListeners();
    if (runtimeUsers == null || serverAccess == null) {
      unawaited(refreshServerManagement());
    }
  }

  void openAccessSettings() {
    if (!accessSettingsAvailable) return;
    settingsTab = settingsDestinations.indexOf(SettingsDestination.access);
    remoteConnectionGuideRequested = true;
    section = WorkbenchSection.settings;
    operationNotice = null;
    notifyListeners();
    if (terminalManagement && terminalCommand == null) {
      unawaited(refreshTerminalCommand());
    }
    if (serverManagement && (runtimeUsers == null || serverAccess == null)) {
      unawaited(refreshServerManagement());
    }
  }

  Future<void> refreshServerManagement({bool quiet = false}) async {
    if (_disposed ||
        !serverManagement ||
        serverManagementLoading ||
        runtimeUserMutating) {
      return;
    }
    if (!quiet) {
      serverManagementLoading = true;
      serverManagementError = null;
      notifyListeners();
    }
    final includeUsage = !quiet && section == WorkbenchSection.usage;
    try {
      final requests = <Future<Object>>[
        _api.serverAccess(),
        _api.runtimeUsers(),
      ];
      if (includeUsage) requests.add(_api.runtimeUsage(_usageQuery()));
      final updated = await Future.wait<Object>(requests);
      if (_disposed) return;
      serverAccess = updated[0] as RuntimeServerAccess;
      runtimeUsers = List<RuntimeUser>.unmodifiable(
        updated[1] as List<RuntimeUser>,
      );
      if (includeUsage) {
        runtimeUsage = updated[2] as RuntimeUsageReport;
      }
      serverManagementLoading = false;
      serverManagementError = null;
      notifyListeners();
    } catch (error) {
      if (_disposed || quiet) return;
      serverManagementLoading = false;
      serverManagementError = _describeError(error);
      notifyListeners();
    }
  }

  RuntimeUsageQuery _usageQuery() {
    final now = _clock().toUtc();
    final until = DateTime.utc(
      now.year,
      now.month,
      now.day,
    ).add(const Duration(days: 1));
    final from = until.subtract(Duration(days: usageRangeDays));
    String civilDate(DateTime value) => [
      value.year.toString().padLeft(4, '0'),
      value.month.toString().padLeft(2, '0'),
      value.day.toString().padLeft(2, '0'),
    ].join('-');
    return RuntimeUsageQuery(
      from: civilDate(from),
      until: civilDate(until),
      timeZone: 'UTC',
    );
  }

  Future<bool> createRuntimeUser({
    required String username,
    required String password,
  }) async {
    if (_disposed || !serverManagement || runtimeUserMutating) return false;
    runtimeUserMutating = true;
    serverManagementError = null;
    notifyListeners();
    try {
      final created = await _api.createRuntimeUser(
        username: username,
        password: password,
      );
      if (_disposed) return false;
      runtimeUsers = List<RuntimeUser>.unmodifiable(
        [...?runtimeUsers, created]
          ..sort((left, right) => left.username.compareTo(right.username)),
      );
      runtimeUserMutating = false;
      notifyListeners();
      unawaited(refreshServerManagement(quiet: true));
      return true;
    } catch (error) {
      if (_disposed) return false;
      serverManagementError = _describeError(error);
      runtimeUserMutating = false;
      notifyListeners();
      return false;
    }
  }

  Future<bool> setRuntimeUserEnabled(
    String userId, {
    required bool enabled,
  }) async {
    if (_disposed || !serverManagement || runtimeUserMutating) return false;
    runtimeUserMutating = true;
    serverManagementError = null;
    notifyListeners();
    try {
      final updated = await _api.setRuntimeUserEnabled(
        userId,
        enabled: enabled,
      );
      if (_disposed) return false;
      runtimeUsers = List<RuntimeUser>.unmodifiable([
        for (final user in runtimeUsers ?? const <RuntimeUser>[])
          if (user.id == userId) updated else user,
      ]);
      runtimeUserMutating = false;
      notifyListeners();
      unawaited(refreshServerManagement(quiet: true));
      return true;
    } catch (error) {
      if (_disposed) return false;
      serverManagementError = _describeError(error);
      runtimeUserMutating = false;
      notifyListeners();
      return false;
    }
  }

  Future<bool> replaceRuntimeUserPassword({
    required String userId,
    required String password,
  }) async {
    if (_disposed || !serverManagement || runtimeUserMutating) return false;
    runtimeUserMutating = true;
    serverManagementError = null;
    notifyListeners();
    try {
      final updated = await _api.replaceRuntimeUserPassword(
        userId: userId,
        password: password,
      );
      if (_disposed) return false;
      runtimeUsers = List<RuntimeUser>.unmodifiable([
        for (final user in runtimeUsers ?? const <RuntimeUser>[])
          if (user.id == updated.id) updated else user,
      ]);
      runtimeUserMutating = false;
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      runtimeUserMutating = false;
      serverManagementError = _describeError(error);
      notifyListeners();
      return false;
    }
  }

  Future<bool> setRuntimeUserPolicy({
    required RuntimeUser user,
    required List<String> allowedEnvironmentIds,
    required int dailyAgentApiCallWarning,
    required int dailyTokenWarning,
  }) async {
    if (_disposed || !serverManagement || runtimeUserMutating) return false;
    runtimeUserMutating = true;
    serverManagementError = null;
    notifyListeners();
    try {
      final updated = await _api.setRuntimeUserPolicy(
        userId: user.id,
        allowedEnvironmentIds: allowedEnvironmentIds,
        dailyAgentApiCallWarning: dailyAgentApiCallWarning,
        dailyTokenWarning: dailyTokenWarning,
      );
      if (_disposed) return false;
      runtimeUsers = List<RuntimeUser>.unmodifiable([
        for (final candidate in runtimeUsers ?? const <RuntimeUser>[])
          if (candidate.id == updated.id) updated else candidate,
      ]);
      runtimeUserMutating = false;
      notifyListeners();
      unawaited(refreshServerManagement(quiet: true));
      return true;
    } catch (error) {
      if (_disposed) return false;
      runtimeUserMutating = false;
      serverManagementError = _describeError(error);
      notifyListeners();
      return false;
    }
  }

  Future<void> refreshTerminalCommand({bool quiet = false}) async {
    if (_disposed || terminalCommandLoading || terminalCommandMutating) return;
    if (!quiet) {
      terminalCommandLoading = true;
      terminalCommandError = null;
      notifyListeners();
    }
    try {
      final updated = await _terminalCommands.inspect();
      if (_disposed) return;
      terminalCommand = updated;
      terminalCommandLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || quiet) return;
      terminalCommandLoading = false;
      terminalCommandError = _terminalCommandError(error);
      notifyListeners();
    }
  }

  // Once per App startup, maintain only an existing receipt-owned link.
  // Never install a new command or replace a package-manager/user-owned file.
  Future<void> _maintainTerminalCommand() async {
    await refreshTerminalCommand();
    if (_disposed) return;
    final current = terminalCommand;
    final operation = current?.canRefresh == true
        ? TerminalCommandOperation.refresh
        : current?.canRepair == true
        ? TerminalCommandOperation.repair
        : null;
    if (operation == null) {
      if (current != null &&
          !current.canInstall &&
          current.state != TerminalCommandState.current) {
        terminalCommandNotice = 'terminal.attention';
        notifyListeners();
      }
      return;
    }
    if (await changeTerminalCommand(operation)) {
      terminalCommandNotice = operation == TerminalCommandOperation.refresh
          ? 'terminal.notice.auto_refreshed'
          : 'terminal.notice.auto_repaired';
      notifyListeners();
    }
  }

  Future<void> refreshPendingApprovals({bool quiet = false}) async {
    if (_disposed || pendingApprovalsLoading || networkMutating) return;
    if (!quiet) {
      pendingApprovalsLoading = true;
      approvalAttentionError = null;
      notifyListeners();
    }
    try {
      final updated = await _api.pendingApprovals();
      if (_disposed) return;
      final changed = !_sameApprovals(
        pendingApprovals ?? const <ApprovalRecord>[],
        updated,
      );
      pendingApprovals = updated;
      final clearedError = approvalAttentionError != null;
      approvalAttentionError = null;
      pendingApprovalsLoading = false;
      if (!quiet || changed || clearedError) notifyListeners();
    } catch (error) {
      if (_disposed || quiet) return;
      approvalAttentionError = _describeError(error);
      pendingApprovalsLoading = false;
      notifyListeners();
    }
  }

  Future<bool> changeTerminalCommand(TerminalCommandOperation operation) async {
    final current = terminalCommand;
    final allowed =
        current != null &&
        switch (operation) {
          TerminalCommandOperation.install => current.canInstall,
          TerminalCommandOperation.refresh => current.canRefresh,
          TerminalCommandOperation.repair => current.canRepair,
          TerminalCommandOperation.remove => current.canRemove,
        };
    if (_disposed || terminalCommandMutating || !allowed) return false;
    terminalCommandMutating = true;
    terminalCommandError = null;
    terminalCommandNotice = null;
    notifyListeners();
    try {
      final updated = await _terminalCommands.execute(operation);
      if (_disposed) return false;
      terminalCommand = updated;
      terminalCommandNotice = switch (operation) {
        TerminalCommandOperation.install => 'terminal.notice.installed',
        TerminalCommandOperation.refresh => 'terminal.notice.refreshed',
        TerminalCommandOperation.repair => 'terminal.notice.repaired',
        TerminalCommandOperation.remove => 'terminal.notice.removed',
      };
      terminalCommandMutating = false;
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      final original = _terminalCommandError(error);
      try {
        terminalCommand = await _terminalCommands.inspect();
      } on Object {
        // Preserve the mutation failure; reconciliation is best effort only.
      }
      if (_disposed) return false;
      terminalCommandError = original;
      terminalCommandMutating = false;
      notifyListeners();
      return false;
    }
  }

  void clearTerminalCommandMessage() {
    terminalCommandError = null;
    terminalCommandNotice = null;
    notifyListeners();
  }

  Future<ExchangeDetail?> loadExchangeDetail(
    String exchangeId, {
    String contentView = 'incremental',
    bool refresh = false,
  }) {
    final key = '$exchangeId:$contentView';
    if (!refresh) {
      final cached = exchangeDetail(exchangeId, contentView: contentView);
      if (cached != null) return Future<ExchangeDetail?>.value(cached);
    }
    if (!refresh) {
      final active = _exchangeLoads[key];
      if (active != null) return active;
    }
    final generation = ++_exchangeLoadGeneration;
    _exchangeLoadGenerations[key] = generation;
    _exchangeErrors.remove(key);
    final load = _fetchExchangeDetail(
      key,
      exchangeId,
      contentView: contentView,
      generation: generation,
    );
    _exchangeLoads[key] = load;
    notifyListeners();
    return load;
  }

  Future<ExchangeDetail?> _fetchExchangeDetail(
    String key,
    String exchangeId, {
    required String contentView,
    required int generation,
  }) async {
    try {
      final detail = await _api.exchange(exchangeId, contentView: contentView);
      if (_disposed || _exchangeLoadGenerations[key] != generation) {
        return detail;
      }
      _exchangeDetails.remove(key);
      _exchangeDetails[key] = detail;
      _trimExchangeDetailCache();
      return detail;
    } catch (error) {
      if (_disposed || _exchangeLoadGenerations[key] != generation) {
        return null;
      }
      _exchangeErrors[key] = _describeError(error);
      return null;
    } finally {
      if (_exchangeLoadGenerations[key] == generation) {
        _exchangeLoadGenerations.remove(key);
        _exchangeLoads.remove(key);
        if (!_disposed) notifyListeners();
      }
    }
  }

  void _trimExchangeDetailCache() {
    while (_exchangeDetails.keys.where((key) => key.endsWith(':full')).length >
        _fullExchangeDetailCacheLimit) {
      final oldestFull = _exchangeDetails.keys.firstWhere(
        (key) => key.endsWith(':full'),
      );
      _exchangeDetails.remove(oldestFull);
    }
    while (_exchangeDetails.length > _exchangeDetailCacheLimit) {
      _exchangeDetails.remove(_exchangeDetails.keys.first);
    }
    var bytes = _exchangeDetails.values.fold<int>(
      0,
      (total, detail) => total + _exchangeDetailBytes(detail),
    );
    // Keep the newly loaded item even when one exchange exceeds the budget.
    while (bytes > _exchangeDetailCacheByteLimit &&
        _exchangeDetails.length > 1) {
      final removed = _exchangeDetails.remove(_exchangeDetails.keys.first)!;
      bytes -= _exchangeDetailBytes(removed);
    }
  }

  // ponytail: content-size estimate, not Dart heap size; profile RSS before
  // adding per-object accounting.
  static int _exchangeDetailBytes(ExchangeDetail detail) {
    final content = detail.content;
    final request = content.request;
    Iterable<ExchangeContentBlock> blocks() sync* {
      if (request != null) {
        yield* request.system;
        for (final message in request.messages) {
          yield* message.blocks;
        }
      }
      if (content.response case final response?) yield* response.blocks;
    }

    return blocks().fold<int>(
      1024,
      (total, block) =>
          total + 256 + max(block.originalSize, block.text?.length ?? 0),
    );
  }

  Future<RawEvidencePage?> loadRawEvidence(
    String exchangeId, {
    bool refresh = false,
  }) async {
    if (!refresh) {
      final cached = _rawEvidencePages.remove(exchangeId);
      if (cached != null) {
        _rawEvidencePages[exchangeId] = cached;
        return cached;
      }
    }
    if (_loadingRawEvidence.contains(exchangeId)) return null;
    final generation = _rawEvidenceGeneration;
    _loadingRawEvidence.add(exchangeId);
    _rawEvidenceErrors.remove(exchangeId);
    notifyListeners();
    try {
      final page = await _api.rawEvidence(exchangeId);
      if (_disposed || generation != _rawEvidenceGeneration) return page;
      _rawEvidencePages.remove(exchangeId);
      _rawEvidencePages[exchangeId] = page;
      while (_rawEvidencePages.length > 64) {
        _rawEvidencePages.remove(_rawEvidencePages.keys.first);
      }
      _loadingRawEvidence.remove(exchangeId);
      notifyListeners();
      return page;
    } catch (error) {
      if (_disposed || generation != _rawEvidenceGeneration) return null;
      _loadingRawEvidence.remove(exchangeId);
      _rawEvidenceErrors[exchangeId] = _describeError(error);
      notifyListeners();
      return null;
    }
  }

  Future<RevealedRawEvidence?> revealRawEvidence({
    required String exchangeId,
    required String envelopeId,
  }) async {
    _rawEvidenceErrors.remove(exchangeId);
    notifyListeners();
    try {
      final revealed = await _api.revealRawEvidence(envelopeId: envelopeId);
      if (_disposed) {
        revealed.clearBody();
        return null;
      }
      return revealed;
    } catch (error) {
      if (_disposed) return null;
      _rawEvidenceErrors[exchangeId] = _describeError(error);
      notifyListeners();
      return null;
    }
  }

  Future<String> revealRawHeader({
    required String envelopeId,
    required String name,
  }) async {
    final generation = _rawEvidenceGeneration;
    final value = await _api.revealRawHeader(
      envelopeId: envelopeId,
      name: name,
    );
    if (_disposed || generation != _rawEvidenceGeneration) {
      throw const ControlContractException(
        'Raw header view is no longer active',
      );
    }
    return value;
  }

  Future<bool> copyMessageTransformSample(String exchangeId) async {
    final activity = selectedActivities
        .where((value) => value.id == exchangeId)
        .firstOrNull;
    final captured = await loadMessageTransformSample(
      exchangeId,
      activity: activity,
    );
    if (captured == null) return false;
    capturedMessageTransformSample = captured;
    section = WorkbenchSection.codeLibrary;
    notifyListeners();
    return true;
  }

  Future<CapturedMessageTransformSample?> loadMessageTransformSample(
    String exchangeId, {
    ActivityRecord? activity,
  }) async {
    final page = await loadRawEvidence(exchangeId);
    if (page == null) return null;
    final pair = _messageTransformSamplePair(page);
    if (pair == null) {
      _rawEvidenceErrors[exchangeId] = 'message_transform_sample_unavailable';
      notifyListeners();
      return null;
    }
    RevealedRawEvidence? requestReveal;
    RevealedRawEvidence? responseReveal;
    try {
      final revealed = await Future.wait([
        _api.revealRawEvidence(envelopeId: pair.request.envelopeId),
        _api.revealRawEvidence(envelopeId: pair.response.envelopeId),
      ]);
      requestReveal = revealed[0];
      responseReveal = revealed[1];
      final captured = CapturedMessageTransformSample.fromRawEvidence(
        request: requestReveal,
        response: responseReveal,
        runtime: _messageTransformRuntime(activity),
      );
      _rawEvidenceErrors.remove(exchangeId);
      notifyListeners();
      return captured;
    } on Object catch (error) {
      _rawEvidenceErrors[exchangeId] = _describeError(error);
      notifyListeners();
      return null;
    } finally {
      requestReveal?.clearBody();
      responseReveal?.clearBody();
    }
  }

  MessageTransformTestRuntime? _messageTransformRuntime(
    ActivityRecord? activity,
  ) {
    if (activity == null) return null;
    final run = data?.captures
        .where((value) => value.captureRunId == activity.captureRunId)
        .firstOrNull
        ?.managedRun;
    return MessageTransformTestRuntime(
      userName: run?.localUserLabel ?? '',
      homeDirectory: run?.homeDirectory ?? '',
      operatingSystem: run?.operatingSystem ?? '',
      operatingSystemVersion: run?.operatingSystemVersion ?? '',
      architecture: run?.architecture ?? '',
      timeZone: run?.timeZone ?? '',
      workspaceRoot: run?.cwd ?? '',
      workspaceLabel: run?.workspaceLabel ?? '',
      turnStartedAt: activity.occurredAt,
    );
  }

  bool canCopyMessageTransformSample(String exchangeId) =>
      _messageTransformSamplePair(rawEvidence(exchangeId)) != null;

  void clearMessageTransformSample() {
    if (capturedMessageTransformSample == null) return;
    capturedMessageTransformSample = null;
    notifyListeners();
  }

  Future<void> refreshNetwork() => _refreshNetwork();

  Future<void> _refreshNetwork({bool quiet = false}) async {
    if (_disposed || networkLoading || networkMutating) return;
    if (!quiet) {
      networkLoading = true;
      _clearNetworkError();
      notifyListeners();
    }
    try {
      final updated = await _api.loadNetwork();
      if (_disposed) return;
      networkData = updated;
      pendingApprovals = updated.approvals;
      approvalAttentionError = null;
      networkLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || quiet) return;
      networkLoading = false;
      _setNetworkError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreConnections() async {
    final current = networkData;
    final cursor = current?.connections.nextCursor;
    if (current == null || cursor == null || networkLoading) return;
    networkLoading = true;
    _clearNetworkError();
    notifyListeners();
    try {
      final page = await _api.connections(cursor: cursor);
      if (_disposed) return;
      networkData = NetworkData(
        approvals: current.approvals,
        connections: ConnectionPage(
          items: [...current.connections.items, ...page.items],
          nextCursor: page.nextCursor,
        ),
        egressAttempts: current.egressAttempts,
        rules: current.rules,
      );
      networkLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed) return;
      networkLoading = false;
      _setNetworkError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreEgressAttempts() async {
    final current = networkData;
    final cursor = current?.egressAttempts.nextCursor;
    if (current == null || cursor == null || networkLoading) return;
    networkLoading = true;
    _clearNetworkError();
    notifyListeners();
    try {
      final page = await _api.egressAttempts(cursor: cursor);
      if (_disposed) return;
      networkData = NetworkData(
        approvals: current.approvals,
        connections: current.connections,
        egressAttempts: EgressAttemptPage(
          items: [...current.egressAttempts.items, ...page.items],
          nextCursor: page.nextCursor,
        ),
        rules: current.rules,
      );
      networkLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed) return;
      networkLoading = false;
      _setNetworkError(error);
      notifyListeners();
    }
  }

  Future<bool> decideApproval(
    ApprovalRecord approval,
    ApprovalChoice choice,
  ) async {
    final current = networkData;
    if (current == null || networkMutating) return false;
    networkMutating = true;
    _clearNetworkError();
    networkNotice = null;
    notifyListeners();
    try {
      final resolved = await _api.decideApproval(
        approval: approval,
        choice: choice,
      );
      if (_disposed) return false;
      networkData = NetworkData(
        approvals: current.approvals
            .where((candidate) => candidate.id != resolved.id)
            .toList(growable: false),
        connections: current.connections,
        egressAttempts: current.egressAttempts,
        rules: current.rules,
      );
      pendingApprovals = pendingApprovals
          ?.where((candidate) => candidate.id != resolved.id)
          .toList(growable: false);
      networkNotice = resolved.state == 'allowed'
          ? 'network.approval_allowed'
          : 'network.approval_denied';
      networkMutating = false;
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      networkMutating = false;
      _setNetworkError(error);
      notifyListeners();
      return false;
    }
  }

  Future<bool> replaceConnectionRules({
    required ConnectionRuleSet base,
    required List<ConnectionRule> rules,
    required String mode,
  }) async {
    final current = networkData;
    if (current == null || networkMutating) return false;
    networkMutating = true;
    _clearNetworkError();
    networkNotice = null;
    notifyListeners();
    try {
      final updated = await _api.replaceConnectionRules(
        // The draft must be fenced by the revision it was created from. A
        // background poll may already have observed a newer rule set; using
        // that revision here would turn an honest CAS into a lost update.
        current: base,
        rules: rules,
        mode: mode,
      );
      if (_disposed) return false;
      networkData = NetworkData(
        approvals: current.approvals,
        connections: current.connections,
        egressAttempts: current.egressAttempts,
        rules: updated,
      );
      networkMutating = false;
      networkNotice = 'network.rules_saved';
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      networkMutating = false;
      _setNetworkError(error);
      notifyListeners();
      return false;
    }
  }

  Future<UpstreamEndpoint?> createUpstreamEndpoint({
    required String displayName,
    required String origin,
    required List<String> backendProtocols,
  }) async {
    final current = data;
    if (current == null || !_beginInventoryMutation()) return null;
    try {
      final created = await _api.createUpstreamEndpoint(
        id: 'target.custom.${_newUuid()}',
        displayName: displayName.trim(),
        origin: origin.trim(),
        backendProtocols: backendProtocols,
      );
      if (_disposed) return null;
      final endpoints = [...current.endpoints, created]
        ..sort((left, right) => left.id.compareTo(right.id));
      data = _dashboardWith(current, endpoints: endpoints);
      selectedEndpointId = created.id;
      inventoryMutating = false;
      inventoryNotice = 'endpoint_created';
      notifyListeners();
      return created;
    } catch (error) {
      if (_disposed) return null;
      inventoryMutating = false;
      _setInventoryError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ProviderAccount?> createProviderAccount({
    required UpstreamEndpoint endpoint,
    required String displayName,
    required String kind,
    required String secret,
    String codexAuthJson = '',
    required ProviderAccountHeaderPolicy headerPolicy,
  }) async {
    final current = data;
    if (current == null ||
        !endpoint.accountKinds.contains(kind) ||
        !_beginInventoryMutation()) {
      return null;
    }
    try {
      final provider = switch (kind) {
        'anthropic_api_key' => 'anthropic',
        'bearer_token' => 'bearer',
        'codex_oauth' => 'codex',
        _ => 'account',
      };
      final created = await _api.createProviderAccount(
        id: 'account.$provider.${_newUuid()}',
        displayName: displayName.trim(),
        upstreamEndpointId: endpoint.id,
        unlinked: true,
        kind: kind,
        secret: secret,
        codexAuthJson: codexAuthJson,
        headerPolicy: headerPolicy,
      );
      if (_disposed) return null;
      final accounts = [...current.accounts, created]
        ..sort((left, right) => left.id.compareTo(right.id));
      data = _dashboardWith(current, accounts: accounts);
      inventoryMutating = false;
      inventoryNotice = 'account_created';
      notifyListeners();
      return created;
    } catch (error) {
      if (_disposed) return null;
      inventoryMutating = false;
      _setInventoryError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ProviderAccount?> setProviderAccountNote(
    ProviderAccount account,
    String note,
  ) async {
    if (!_beginInventoryMutation()) return null;
    try {
      final updated = await _api.setProviderAccountNote(account, note);
      if (_disposed || data == null) return null;
      data = _dashboardWith(
        data!,
        accounts: [
          for (final candidate in data!.accounts)
            if (candidate.id == updated.id) updated else candidate,
        ],
      );
      return updated;
    } catch (error) {
      if (error is ControlProblem && error.status == 409) {
        // Keep the mutation guard while loading the competing edit. The normal
        // dashboard refresh deliberately skips work during inventory mutations.
        try {
          final latest = await _api.loadDashboard();
          if (!_disposed && data != null) {
            data = _dashboardWith(data!, accounts: latest.accounts);
          }
        } catch (_) {
          // The editor still owns its unsaved draft if the reload also fails.
        }
        if (!_disposed) inventoryError = 'provider_accounts.note.conflict';
      } else if (!_disposed) {
        inventoryError = 'provider_accounts.note.failed';
      }
      return null;
    } finally {
      if (!_disposed) {
        inventoryMutating = false;
        notifyListeners();
      }
    }
  }

  Future<bool> setProviderAccountAssociation({
    required ProviderAccount account,
    required UpstreamEndpoint endpoint,
    required bool linked,
  }) async {
    if (!_beginInventoryMutation()) return false;
    try {
      final updated = await _api.setProviderAccountAssociation(
        account:
            data!.accounts
                .where((value) => value.id == account.id)
                .firstOrNull ??
            account,
        endpoint: endpoint,
        linked: linked,
      );
      if (_disposed || data == null) return false;
      data = _dashboardWith(
        data!,
        accounts: [
          for (final candidate in data!.accounts)
            if (candidate.id == updated.id) updated else candidate,
        ],
      );
      inventoryNotice = linked ? 'account_linked' : 'account_unlinked';
      return true;
    } catch (error) {
      await _reconcileAccountConflict(error);
      if (!_disposed) _setInventoryError(error);
      return false;
    } finally {
      if (!_disposed) {
        inventoryMutating = false;
        notifyListeners();
      }
    }
  }

  Future<CodexLogin> startCodexLogin({
    required UpstreamEndpoint endpoint,
    required String displayName,
    required String callbackMode,
  }) => _api.startCodexLogin(
    accountId: 'account.codex.${_newUuid()}',
    upstreamEndpointId: endpoint.id,
    displayName: displayName.trim(),
    callbackMode: callbackMode,
  );

  Future<CodexLogin> codexLoginStatus(String loginId) =>
      _api.codexLoginStatus(loginId);
  Future<CodexLogin> completeCodexLogin(String loginId, String callbackUrl) =>
      _api.completeCodexLogin(loginId, callbackUrl);
  Future<void> cancelCodexLogin(String loginId) =>
      _api.cancelCodexLogin(loginId);

  String? refreshingProviderAccountId;

  Future<ProviderAccount?> refreshProviderAccountCredential(
    ProviderAccount account,
  ) async {
    if (data == null ||
        account.kind != 'codex_oauth' ||
        !account.usable ||
        !_beginInventoryMutation()) {
      return null;
    }
    refreshingProviderAccountId = account.id;
    notifyListeners();
    try {
      final updated = await _api.refreshProviderAccountCredential(account);
      if (_disposed) return null;
      final current = data!;
      data = _dashboardWith(
        current,
        accounts: current.accounts
            .map(
              (candidate) => candidate.id == updated.id ? updated : candidate,
            )
            .toList(growable: false),
      );
      inventoryNotice = 'credential_refreshed';
      return updated;
    } catch (error) {
      if (_disposed) return null;
      _setInventoryError(error);
      if (error is ControlProblem &&
          error.reasonCode == 'provider_account_conflict') {
        inventoryError = 'provider_accounts.refresh.conflict';
      }
      // Permanent refresh failures can advance the stored reconnect state.
      // Reconcile safe account metadata, without reading quota or history.
      try {
        final refreshed = await _api.loadDashboard();
        if (!_disposed) {
          data = refreshed;
          _repairDashboardSelections(refreshed);
        }
      } catch (_) {
        /* Keep the refresh error if reconciliation also fails. */
      }
      return null;
    } finally {
      if (!_disposed) {
        inventoryMutating = false;
        refreshingProviderAccountId = null;
        notifyListeners();
      }
    }
  }

  Future<ProviderAccount?> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
    String codexAuthJson = '',
    required ProviderAccountHeaderPolicy headerPolicy,
  }) async {
    final current = data;
    if (current == null || !_beginInventoryMutation()) return null;
    try {
      final updated = await _api.replaceProviderAccountCredential(
        account: account,
        secret: secret,
        codexAuthJson: codexAuthJson,
        headerPolicy: headerPolicy,
      );
      if (_disposed) return null;
      final accounts = current.accounts
          .map((candidate) => candidate.id == updated.id ? updated : candidate)
          .toList(growable: false);
      data = _dashboardWith(current, accounts: accounts);
      inventoryMutating = false;
      inventoryNotice = 'credential_replaced';
      notifyListeners();
      return updated;
    } catch (error) {
      if (_disposed) return null;
      inventoryMutating = false;
      _setInventoryError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ProviderAccountDeleteResult?> deleteProviderAccount(
    ProviderAccount account,
  ) async {
    final current = data;
    if (current == null || !_beginInventoryMutation()) return null;
    try {
      final result = await _api.deleteProviderAccount(account);
      if (_disposed) return null;
      if (result.deleted) {
        data = _dashboardWith(
          current,
          accounts: current.accounts
              .where((candidate) => candidate.id != account.id)
              .toList(growable: false),
        );
        inventoryNotice = 'account_deleted';
      }
      inventoryMutating = false;
      notifyListeners();
      return result;
    } catch (error) {
      if (_disposed) return null;
      inventoryMutating = false;
      _setInventoryError(error);
      notifyListeners();
      return null;
    }
  }

  /// Retires an Environment. Returns the runtime's answer so the caller can
  /// show the holders when it refused; a null means the call itself failed.
  Future<DeletionOutcome?> deleteEnvironment(String environmentId) =>
      _runDeletion(
        () => _api.deleteEnvironment(environmentId),
        onDeleted: (current) => _dashboardWith(
          current,
          environments: current.environments
              .where((candidate) => candidate.id != environmentId)
              .toList(growable: false),
        ),
        notice: 'environment_deleted',
      );

  Future<DeletionOutcome?> deleteUpstreamEndpoint(String endpointId) =>
      _runDeletion(
        () => _api.deleteUpstreamEndpoint(endpointId),
        onDeleted: (current) => _dashboardWith(
          current,
          endpoints: current.endpoints
              .where((candidate) => candidate.id != endpointId)
              .toList(growable: false),
        ),
        notice: 'endpoint_deleted',
      );

  Future<DeletionOutcome?> deleteCapture(String captureKey) => _runDeletion(
    () => _api.deleteCapture(captureKey),
    onDeleted: (current) => _dashboardWith(
      current,
      captures: current.captures
          .where((candidate) => candidate.key != captureKey)
          .toList(growable: false),
    ),
    notice: 'capture_deleted',
    afterDeleted: () {
      _invalidateEvidenceCaches();
      if (selectedCaptureKey == captureKey) {
        selectedCaptureKey = null;
        _resetCaptureDetail();
      }
    },
    reloadCaptureDetail: true,
  );

  Future<bool> applyLatestSelectedCaptureEnvironment() async {
    final capture = selectedCapture;
    final current = selectedAssignment;
    final latest = data?.environments
        .where((environment) => environment.id == current?.environmentId)
        .firstOrNull;
    if (capture == null ||
        !capture.running ||
        current == null ||
        latest == null ||
        latest.revision <= current.environmentRevision ||
        mutating) {
      return false;
    }
    final captureKey = capture.key;
    mutating = true;
    errorMessage = null;
    operationNotice = null;
    notifyListeners();
    try {
      final updated = await _api.applyLatestCaptureEnvironment(current);
      if (_disposed) return false;
      if (selectedCaptureKey == captureKey) {
        selectedAssignment = updated;
        operationNotice = 'capture_environment_applied';
      }
      mutating = false;
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      if (selectedCaptureKey == captureKey) {
        errorMessage = _describeError(error);
      }
      mutating = false;
      notifyListeners();
      return false;
    }
  }

  Future<DeletionOutcome?> clearEvidence() => _runDeletion(
    _api.clearEvidence,
    onDeleted: (current) => _dashboardWith(current, captures: const []),
    notice: 'archive_cleared',
    afterDeleted: () {
      selectedCaptureKey = null;
      _resetCaptureDetail();
      _invalidateEvidenceCaches();
      unawaited(refreshStorageLocation());
    },
  );

  /// Every destructive action runs through here, so they share one mutation
  /// gate, one error surface and one rule: local state changes only when the
  /// runtime says the delete happened. A refusal leaves the workbench exactly
  /// as it was, which is what makes it safe to show the holders and let the
  /// user try again.
  Future<DeletionOutcome?> _runDeletion(
    Future<DeletionOutcome> Function() call, {
    required DashboardData Function(DashboardData current) onDeleted,
    required String notice,
    void Function()? afterDeleted,
    bool reloadCaptureDetail = false,
  }) async {
    final current = data;
    if (current == null || !_beginInventoryMutation()) return null;
    final generation = _dashboardGeneration;
    try {
      final outcome = await call();
      if (_disposed || generation != _dashboardGeneration) return null;
      if (outcome.deleted) {
        data = onDeleted(current);
        afterDeleted?.call();
        _repairDashboardSelections(data!);
        inventoryNotice = notice;
        notifyListeners();
        try {
          final refreshed = await _api.loadDashboard();
          if (_disposed || generation != _dashboardGeneration) return outcome;
          data = refreshed;
          _repairDashboardSelections(refreshed);
        } catch (error) {
          if (_disposed || generation != _dashboardGeneration) return outcome;
          // The deletion is already authoritative. Keep the local projection
          // honest and surface only the failed reconciliation.
          _setInventoryError(error);
          inventoryError = 'error.deleted_refresh_failed';
        }
      }
      inventoryMutating = false;
      notifyListeners();
      if (outcome.deleted && reloadCaptureDetail) {
        final capture = selectedCapture;
        if (capture != null) unawaited(_loadCaptureDetail(capture));
      }
      return outcome;
    } catch (error) {
      if (_disposed) return null;
      inventoryMutating = false;
      _setInventoryError(error);
      notifyListeners();
      return null;
    }
  }

  void _repairDashboardSelections(
    DashboardData updated, {
    bool forceCaptureDefault = false,
  }) {
    final previousCaptureKey = selectedCaptureKey;
    final captureExists =
        previousCaptureKey != null &&
        updated.captures.any((capture) => capture.key == previousCaptureKey);
    if (forceCaptureDefault || !captureExists) {
      final running =
          updated.captures
              .where((capture) => capture.running)
              .toList(growable: false)
            ..sort((left, right) => right.updatedAt.compareTo(left.updatedAt));
      final history =
          updated.captures
              .where((capture) => !capture.running)
              .toList(growable: false)
            ..sort((left, right) => right.updatedAt.compareTo(left.updatedAt));
      selectedCaptureKey = running.firstOrNull?.key ?? history.firstOrNull?.key;
    }
    if (selectedCaptureKey != previousCaptureKey) {
      _resetCaptureDetail();
    }

    final environmentExists =
        selectedEnvironmentId != null &&
        updated.environments.any(
          (environment) => environment.id == selectedEnvironmentId,
        );
    if (selectedEnvironmentRevision == null && !environmentExists) {
      _environmentRevisionGeneration += 1;
      selectedEnvironmentId = updated.environments.firstOrNull?.id;
      historicalEnvironment = null;
      environmentRevisionLoading = false;
      reviewedEnvironmentDraft = null;
      reviewedEnvironmentImpact = null;
      environmentError = null;
      environmentNotice = null;
    }

    final endpointExists =
        selectedEndpointId != null &&
        updated.endpoints.any((endpoint) => endpoint.id == selectedEndpointId);
    if (!endpointExists) {
      selectedEndpointId = updated.endpoints.firstOrNull?.id;
    }
  }

  void _resetCaptureDetail() {
    _selectionGeneration += 1;
    selectedAssignment = null;
    selectedCaptureLaunchIncomplete = false;
    selectedACP = null;
    selectedCaptureConversations = null;
    selectedCaptureConversationKey = null;
    selectedCapturePage = null;
    detailLoading = false;
    captureActivitiesLoading = false;
  }

  void _invalidateEvidenceCaches() {
    _exchangeLoadGeneration += 1;
    _exchangeDetails.clear();
    _exchangeLoads.clear();
    _exchangeLoadGenerations.clear();
    _exchangeErrors.clear();
    _rawEvidenceGeneration += 1;
    _rawEvidencePages.clear();
    _loadingRawEvidence.clear();
    _rawEvidenceErrors.clear();
    _captureConversationPages.clear();
  }

  String _captureConversationPageKey(
    String captureKey,
    String conversationKey,
  ) => '$captureKey\u0000$conversationKey';

  ActivityPage? _cachedCaptureConversationPage(
    String captureKey,
    String conversationKey,
  ) {
    final key = _captureConversationPageKey(captureKey, conversationKey);
    final cached = _captureConversationPages.remove(key);
    if (cached != null) _captureConversationPages[key] = cached;
    return cached;
  }

  void _cacheCaptureConversationPage(
    String captureKey,
    String conversationKey,
    ActivityPage page,
  ) {
    final key = _captureConversationPageKey(captureKey, conversationKey);
    _captureConversationPages.remove(key);
    _captureConversationPages[key] = page;
    while (_captureConversationPages.length >
        _captureConversationPageCacheLimit) {
      _captureConversationPages.remove(_captureConversationPages.keys.first);
    }
  }

  ActivityPage _reconcileCaptureConversationPage(
    ActivityPage? retained,
    ActivityPage latest,
  ) {
    if (retained == null) return latest;
    return ActivityPage(
      items: mergePolledWindow(
        retained.items,
        latest.items,
        identity: (item) => item.id,
        recency: (item) => item.occurredAt,
      ),
      // A retained page may include older pages which the newest bounded
      // window cannot describe. Keep its continuation boundary until an
      // explicit load-more request advances it.
      nextCursor: retained.nextCursor,
    );
  }

  void clearInventoryNotice() {
    inventoryNotice = null;
    notifyListeners();
  }

  void clearNetworkNotice() {
    networkNotice = null;
    notifyListeners();
  }

  Future<void> selectCapture(String captureKey) async {
    if (selectedCaptureKey == captureKey && selectedAssignment != null) return;
    selectedCaptureKey = captureKey;
    _resetCaptureDetail();
    operationNotice = null;
    notifyListeners();
    final capture = selectedCapture;
    if (capture != null) await _loadCaptureDetail(capture);
  }

  void selectEnvironment(String environmentId) {
    _environmentRevisionGeneration += 1;
    selectedEnvironmentId = environmentId;
    selectedEnvironmentRevision = null;
    historicalEnvironment = null;
    environmentRevisionLoading = false;
    reviewedEnvironmentDraft = null;
    reviewedEnvironmentImpact = null;
    environmentError = null;
    environmentNotice = null;
    operationNotice = null;
    notifyListeners();
  }

  Future<EnvironmentRecord?> inspectEnvironmentRevision(
    String environmentId,
    int revision, {
    String? expectedDigest,
    bool navigate = true,
  }) async {
    if (_disposed || revision < 1) return null;
    final generation = ++_environmentRevisionGeneration;
    selectedEnvironmentId = environmentId;
    selectedEnvironmentRevision = revision;
    historicalEnvironment = null;
    environmentRevisionLoading = true;
    reviewedEnvironmentDraft = null;
    reviewedEnvironmentImpact = null;
    environmentError = null;
    environmentNotice = null;
    operationNotice = null;
    if (navigate) section = WorkbenchSection.environments;
    notifyListeners();
    try {
      final environment = await _api.environmentRevision(
        environmentId,
        revision,
      );
      if (expectedDigest != null && environment.digest != expectedDigest) {
        throw const ControlContractException(
          'Frozen Environment digest does not match the stored revision',
        );
      }
      if (_disposed || generation != _environmentRevisionGeneration) {
        return null;
      }
      historicalEnvironment = environment;
      environmentRevisionLoading = false;
      notifyListeners();
      return environment;
    } catch (error) {
      if (_disposed || generation != _environmentRevisionGeneration) {
        return null;
      }
      environmentRevisionLoading = false;
      _setEnvironmentError(error);
      notifyListeners();
      return null;
    }
  }

  void showCurrentEnvironment() {
    _environmentRevisionGeneration += 1;
    selectedEnvironmentRevision = null;
    historicalEnvironment = null;
    environmentRevisionLoading = false;
    environmentError = null;
    environmentNotice = null;
    reviewedEnvironmentDraft = null;
    reviewedEnvironmentImpact = null;
    if (selectedEnvironment == null) {
      selectedEnvironmentId = data?.environments.firstOrNull?.id;
    }
    notifyListeners();
  }

  Future<EnvironmentImpact?> reviewSelectedEnvironment(
    EnvironmentDraftInput candidate, {
    EnvironmentRecord? baseEnvironment,
  }) async {
    final environment = baseEnvironment ?? selectedEnvironment;
    if (environment == null ||
        environment.id != selectedEnvironmentId ||
        selectedEnvironmentRevision != null ||
        environment.systemOwned ||
        environmentMutating) {
      return null;
    }
    return _reviewEnvironmentDraft(
      environmentId: environment.id,
      expectedBaseRevision: environment.revision,
      baseClientEndpoints: environment.clientEndpoints,
      candidate: candidate,
      requireCurrentSelection: true,
    );
  }

  Future<EnvironmentImpact?> reviewNewEnvironment(
    String environmentId,
    EnvironmentDraftInput candidate,
  ) => _reviewEnvironmentDraft(
    environmentId: environmentId,
    expectedBaseRevision: 0,
    baseClientEndpoints: const [],
    candidate: candidate,
    requireCurrentSelection: false,
  );

  Future<EnvironmentImpact?> _reviewEnvironmentDraft({
    required String environmentId,
    required int expectedBaseRevision,
    required List<EnvironmentClientEndpoint> baseClientEndpoints,
    required EnvironmentDraftInput candidate,
    required bool requireCurrentSelection,
  }) async {
    if (!_beginEnvironmentMutation()) return null;
    environmentNotice = null;
    reviewedEnvironmentDraft = null;
    reviewedEnvironmentImpact = null;
    notifyListeners();
    try {
      final preparedEndpoints = prepareEnvironmentDraftEndpoints(
        base: baseClientEndpoints,
        edited: candidate.clientEndpoints,
        upstreamEndpoints: data?.endpoints ?? const [],
        availableAccounts: data?.accounts ?? const [],
      );
      var expectedDraftRevision = 0;
      try {
        final existing = await _api.environmentDraft(environmentId);
        if (existing.baseRevision != expectedBaseRevision) {
          throw const ControlProblem(
            status: 409,
            reasonCode: 'revision_conflict',
            messageKey: 'error.revision_conflict',
          );
        }
        expectedDraftRevision = existing.draftRevision;
      } on ControlProblem catch (problem) {
        if (problem.status != 404 ||
            problem.reasonCode != 'environment_draft_not_found') {
          rethrow;
        }
      }
      final draft = await _api.saveEnvironmentDraft(
        environmentId: environmentId,
        expectedBaseRevision: expectedBaseRevision,
        input: candidate.copyWith(
          expectedDraftRevision: expectedDraftRevision,
          clientEndpoints: preparedEndpoints,
        ),
      );
      final impact = await _api.previewEnvironmentDraft(
        environmentId,
        draft.draftRevision,
      );
      if (impact.baseRevision != draft.baseRevision ||
          impact.candidateDigest != draft.candidateDigest) {
        throw const ControlContractException(
          'Environment impact does not match the reviewed draft',
        );
      }
      if (_disposed) return null;
      if (requireCurrentSelection && selectedEnvironmentId != environmentId) {
        environmentMutating = false;
        notifyListeners();
        return null;
      }
      reviewedEnvironmentDraft = draft;
      reviewedEnvironmentImpact = impact;
      environmentMutating = false;
      notifyListeners();
      return impact;
    } catch (error) {
      if (_disposed) return null;
      if (requireCurrentSelection && selectedEnvironmentId != environmentId) {
        environmentMutating = false;
        notifyListeners();
        return null;
      }
      environmentMutating = false;
      _setEnvironmentError(error);
      notifyListeners();
      return null;
    }
  }

  Future<EnvironmentPublishResult?> publishReviewedEnvironment() async {
    final draft = reviewedEnvironmentDraft;
    final impact = reviewedEnvironmentImpact;
    final current = data;
    if (draft == null ||
        impact == null ||
        current == null ||
        environmentMutating ||
        draft.environmentId != impact.environmentId ||
        draft.draftRevision != impact.draftRevision) {
      return null;
    }
    if (!_beginEnvironmentMutation()) return null;
    notifyListeners();
    try {
      final result = await _api.publishEnvironmentDraft(
        draft.environmentId,
        draft.draftRevision,
      );
      if (_disposed) return null;
      final environments =
          [
            for (final candidate in current.environments)
              if (candidate.id != result.environment.id) candidate,
            result.environment,
          ]..sort((left, right) {
            if (left.systemOwned != right.systemOwned) {
              return left.systemOwned ? -1 : 1;
            }
            return left.id.compareTo(right.id);
          });
      data = _dashboardWith(current, environments: environments);
      selectedEnvironmentId = result.environment.id;
      selectedEnvironmentRevision = null;
      historicalEnvironment = null;
      reviewedEnvironmentDraft = null;
      reviewedEnvironmentImpact = null;
      environmentMutating = false;
      environmentNotice = 'environment.published';
      notifyListeners();
      return result;
    } catch (error) {
      if (_disposed) return null;
      environmentMutating = false;
      _setEnvironmentError(error);
      notifyListeners();
      return null;
    }
  }

  void clearEnvironmentReview() {
    reviewedEnvironmentDraft = null;
    reviewedEnvironmentImpact = null;
    environmentError = null;
    notifyListeners();
  }

  void clearEnvironmentNotice() {
    environmentNotice = null;
    notifyListeners();
  }

  void selectEndpoint(String endpointId) {
    selectedEndpointId = endpointId;
    operationNotice = null;
    notifyListeners();
  }

  void setLanguage(AppLanguage value) {
    if (language == value) return;
    language = value;
    notifyListeners();
  }

  void setTheme(WorkbenchTheme value) {
    if (theme == value) return;
    theme = value;
    _onThemeChanged?.call(value);
    notifyListeners();
  }

  WorkbenchPreferences get currentPreferences => WorkbenchPreferences(
    language: language,
    theme: theme,
    section: section,
    selectedCaptureKey: selectedCaptureKey,
    selectedEnvironmentId: selectedEnvironmentId,
    selectedEnvironmentRevision: selectedEnvironmentRevision,
    selectedEndpointId: selectedEndpointId,
  );

  void retryPreferenceSave() {
    if (!_preferencesWritable || _disposed) return;
    _desiredPreferences = null;
    _queuePreferencesIfChanged();
  }

  Future<void> flushPreferences() async {
    if (_pendingPreferences != null && !_preferenceDraining) {
      _startPreferenceDrain();
    }
    await _preferenceDrain;
  }

  @override
  void notifyListeners() {
    if (!_disposed) _queuePreferencesIfChanged();
    super.notifyListeners();
  }

  void _queuePreferencesIfChanged() {
    if (!_preferencesWritable) return;
    final snapshot = currentPreferences;
    if (snapshot == _desiredPreferences) return;
    _desiredPreferences = snapshot;
    _pendingPreferences = snapshot;
    if (!_preferenceDraining) _startPreferenceDrain();
  }

  void _startPreferenceDrain() {
    _preferenceDraining = true;
    _preferenceDrain = _drainPreferenceWrites();
  }

  Future<void> _drainPreferenceWrites() async {
    while (true) {
      final next = _pendingPreferences;
      if (next == null) break;
      _pendingPreferences = null;
      try {
        await _preferencesStore.write(next.encode());
      } on Object {
        _preferenceDraining = false;
        if (!_disposed) {
          preferenceWarning = WorkbenchPreferencesIssue.saveFailed.copyKey;
          super.notifyListeners();
        }
        return;
      }
      if (!_disposed && preferenceWarning != null) {
        preferenceWarning = null;
        super.notifyListeners();
      }
    }
    _preferenceDraining = false;
  }

  Future<void> _loadCaptureDetail(
    CaptureRecord capture, {
    bool quiet = false,
    bool followLatest = false,
  }) async {
    if (quiet && (captureActivitiesLoading || _captureDetailLoads != 0)) return;
    _captureDetailLoads += 1;
    final generation = ++_selectionGeneration;
    final currentPage = selectedCapturePage;
    final currentConversationKey = selectedCaptureConversationKey;
    final previousConversation = selectedCaptureConversation;
    if (!quiet) {
      detailLoading = true;
      errorMessage = null;
      notifyListeners();
    }
    try {
      final values = await Future.wait<Object?>([
        _loadCaptureAssignment(capture),
        _captureConversationPage(capture, limit: 200),
        if (capture.isACP && _api is ACPObservationApi)
          (_api as ACPObservationApi).acpObservation(capture.key)
        else
          Future<ACPRecord?>.value(),
      ]);
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != capture.key) {
        return;
      }
      selectedAssignment = values[0] as CaptureAssignment?;
      selectedACP = values[2] as ACPRecord?;
      selectedCaptureLaunchIncomplete = selectedAssignment == null;
      final conversationPage = values[1]! as ConversationPage;
      selectedCaptureConversations = conversationPage;
      final available = captureConversations;
      if (followLatest && available.isNotEmpty) {
        selectedCaptureConversationKey = available.first.key;
      } else if (!available.any(
        (value) => value.key == selectedCaptureConversationKey,
      )) {
        final migrated =
            previousConversation?.conversation.kind == 'pending_exchange'
            ? available
                  .where(
                    (value) =>
                        value.latest.id == previousConversation!.latest.id,
                  )
                  .firstOrNull
            : null;
        selectedCaptureConversationKey =
            migrated?.key ?? _preferredCaptureConversation(available)?.key;
      }
      final selectedKey = selectedCaptureConversationKey;
      final cached = selectedKey == null
          ? null
          : _cachedCaptureConversationPage(capture.key, selectedKey);
      final latest = selectedKey == null
          ? const ActivityPage(items: [], nextCursor: null)
          : await _captureActivityPage(
              capture,
              conversationId: selectedKey,
              limit: 100,
            );
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != capture.key) {
        return;
      }
      final retained =
          quiet &&
              currentPage != null &&
              currentConversationKey == selectedCaptureConversationKey
          ? currentPage
          : cached;
      selectedCapturePage = _reconcileCaptureConversationPage(retained, latest);
      if (selectedKey != null) {
        _cacheCaptureConversationPage(
          capture.key,
          selectedKey,
          selectedCapturePage!,
        );
      }
      captureActivitiesLoading = false;
      detailLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || generation != _selectionGeneration || quiet) return;
      captureActivitiesLoading = false;
      detailLoading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    } finally {
      _captureDetailLoads -= 1;
    }
  }

  Future<CaptureAssignment?> _loadCaptureAssignment(
    CaptureRecord capture,
  ) async {
    try {
      return await _api.captureAssignment(capture.key);
    } on ControlProblem catch (error) {
      // A failed pre-launch assignment in older runtimes left a finished run
      // with no child or traffic. Other missing assignments remain errors.
      if (error.status == 404 &&
          error.reasonCode == 'capture_assignment_not_found' &&
          !capture.isManual &&
          !capture.running &&
          capture.managedRun?.processId == null &&
          capture.observation == 'waiting_for_traffic') {
        return null;
      }
      rethrow;
    }
  }

  Future<ActivityPage> _captureActivityPage(
    CaptureRecord capture, {
    String? cursor,
    String? conversationId,
    required int limit,
  }) {
    final nativeSession =
        conversationId != null &&
        captureConversations.any(
          (value) =>
              value.key == conversationId &&
              value.conversation.evidence == 'explicit_session',
        );
    return _api.activities(
      cursor: cursor,
      limit: limit,
      // A Capture is one launch boundary. A proven native Client Session may
      // continue through a later launch, so its exact projection is the query
      // authority and the current Capture must not truncate the timeline.
      captureRunId: nativeSession || capture.isManual
          ? null
          : capture.captureRunId,
      manualCaptureId: nativeSession || !capture.isManual ? null : capture.id,
      conversationId: conversationId,
    );
  }

  Future<ConversationPage> _captureConversationPage(
    CaptureRecord capture, {
    String? cursor,
    required int limit,
  }) => _api.conversations(
    cursor: cursor,
    limit: limit,
    captureRunId: capture.isManual ? null : capture.captureRunId,
    manualCaptureId: capture.isManual ? capture.id : null,
  );

  ConversationSummary? _preferredCaptureConversation(
    List<ConversationSummary> values,
  ) =>
      values.where((value) => value.conversation.kind == 'main').firstOrNull ??
      values.where((value) => value.conversation.kind == 'agent').firstOrNull ??
      values.firstOrNull;

  Future<void> selectCaptureConversation(String key) async {
    final capture = selectedCapture;
    if (capture == null ||
        !captureConversations.any((value) => value.key == key) ||
        (selectedCaptureConversationKey == key &&
            selectedCapturePage != null)) {
      return;
    }
    final generation = ++_selectionGeneration;
    final captureKey = capture.key;
    selectedCaptureConversationKey = key;
    final cached = _cachedCaptureConversationPage(captureKey, key);
    selectedCapturePage = cached;
    captureActivitiesLoading = true;
    errorMessage = null;
    notifyListeners();
    try {
      final page = await _captureActivityPage(
        capture,
        conversationId: key,
        limit: 100,
      );
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != captureKey ||
          selectedCaptureConversationKey != key) {
        return;
      }
      selectedCapturePage = _reconcileCaptureConversationPage(cached, page);
      _cacheCaptureConversationPage(captureKey, key, selectedCapturePage!);
      captureActivitiesLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || generation != _selectionGeneration) return;
      captureActivitiesLoading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreSelectedCapture() async {
    final capture = selectedCapture;
    final current = selectedCapturePage;
    final cursor = current?.nextCursor;
    if (capture == null ||
        current == null ||
        cursor == null ||
        captureActivitiesLoading) {
      return;
    }
    final captureKey = capture.key;
    final conversationKey = selectedCaptureConversationKey;
    final generation = _selectionGeneration;
    captureActivitiesLoading = true;
    errorMessage = null;
    notifyListeners();
    try {
      final page = await _captureActivityPage(
        capture,
        cursor: cursor,
        conversationId: selectedCaptureConversationKey,
        limit: 100,
      );
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != captureKey ||
          selectedCaptureConversationKey != conversationKey) {
        return;
      }
      final unique = <String, ActivityRecord>{
        for (final item in current.items) item.id: item,
        for (final item in page.items) item.id: item,
      };
      selectedCapturePage = ActivityPage(
        items: unique.values.toList(growable: false),
        nextCursor: page.nextCursor,
      );
      if (conversationKey != null) {
        _cacheCaptureConversationPage(
          captureKey,
          conversationKey,
          selectedCapturePage!,
        );
      }
      captureActivitiesLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != captureKey ||
          selectedCaptureConversationKey != conversationKey) {
        return;
      }
      captureActivitiesLoading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    }
  }

  Future<bool> revokeSelectedManualCapture() async {
    final capture = selectedCapture;
    if (capture == null || !capture.isManual || !capture.running || mutating) {
      return false;
    }
    mutating = true;
    errorMessage = null;
    operationNotice = null;
    notifyListeners();
    try {
      final current = await _api.manualCaptureState(capture.id);
      if (current.state != 'active') {
        throw const ControlProblem(
          status: 409,
          reasonCode: 'manual_capture_not_active',
          messageKey: 'error.manual_capture_not_active',
        );
      }
      await _api.revokeManualCapture(
        manualCaptureId: capture.id,
        stateTag: current.stateTag,
      );
      final updated = await _api.loadDashboard();
      if (_disposed) return false;
      data = updated;
      operationNotice = 'manual_capture.revoked';
      mutating = false;
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      errorMessage = _describeError(error);
      mutating = false;
      notifyListeners();
      return false;
    }
  }

  Future<ManualCaptureContext?> loadManualCaptureContext(
    String environmentId,
  ) async {
    errorMessage = null;
    notifyListeners();
    try {
      return await _api.manualCaptureContext(environmentId);
    } catch (error) {
      if (_disposed) return null;
      errorMessage = _describeError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ManualCaptureGrantStateTag?> createManualCapture({
    required ManualCaptureContext context,
    required String displayName,
    required String clientClass,
    required String lifetime,
    int? expiresInSeconds,
  }) async {
    if (mutating) return null;
    mutating = true;
    errorMessage = null;
    operationNotice = null;
    notifyListeners();
    try {
      final created = await _api.createManualCapture(
        context: context,
        displayName: displayName.trim(),
        clientClass: clientClass,
        lifetime: lifetime,
        expiresInSeconds: expiresInSeconds,
      );
      final updated = await _api.loadDashboard();
      if (_disposed) return null;
      data = updated;
      selectedCaptureKey = 'manual_capture:${created.grant.capture.id}';
      operationNotice = 'manual_capture.created';
      mutating = false;
      notifyListeners();
      final capture = selectedCapture;
      if (capture != null) await _loadCaptureDetail(capture);
      return created;
    } catch (error) {
      if (_disposed) return null;
      errorMessage = _describeError(error);
      mutating = false;
      notifyListeners();
      return null;
    }
  }

  Future<ManualCaptureGrantStateTag?> rotateSelectedManualCapture() async {
    final capture = selectedCapture;
    if (capture == null || !capture.isManual || !capture.running || mutating) {
      return null;
    }
    mutating = true;
    errorMessage = null;
    operationNotice = null;
    notifyListeners();
    try {
      final current = await _api.manualCaptureState(capture.id);
      if (current.state != 'active') {
        throw const ControlProblem(
          status: 409,
          reasonCode: 'manual_capture_not_active',
          messageKey: 'error.manual_capture_not_active',
        );
      }
      final rotated = await _api.rotateManualCapture(current);
      final updated = await _api.loadDashboard();
      if (_disposed) return null;
      data = updated;
      operationNotice = 'manual_capture.rotated';
      mutating = false;
      notifyListeners();
      return rotated;
    } catch (error) {
      if (_disposed) return null;
      errorMessage = _describeError(error);
      mutating = false;
      notifyListeners();
      return null;
    }
  }

  void clearNotice() {
    operationNotice = null;
    notifyListeners();
  }

  String _describeError(Object error) {
    return switch (error) {
      EnvironmentUpstreamReferenceException() =>
        'error.environment_upstream_changed',
      ControlProblem(reasonCode: 'environment_upstream_stale') =>
        'error.environment_upstream_stale',
      ControlProblem problem => '${problem.reasonCode} (${problem.status})',
      ControlContractException contract => contract.message,
      _ => error.toString(),
    };
  }

  bool _beginInventoryMutation() {
    if (_disposed || data == null || inventoryMutating || environmentMutating) {
      return false;
    }
    _invalidateDashboardReads();
    inventoryMutating = true;
    inventoryError = null;
    inventoryErrorDiagnostic = null;
    inventoryNotice = null;
    notifyListeners();
    return true;
  }

  bool _beginEnvironmentMutation() {
    if (_disposed || data == null || inventoryMutating || environmentMutating) {
      return false;
    }
    _invalidateDashboardReads();
    environmentMutating = true;
    environmentError = null;
    environmentErrorDiagnostic = null;
    return true;
  }

  void _invalidateDashboardReads() {
    // Reads started before a write cannot roll back the successful local result.
    ++_dashboardGeneration;
    loading = false;
    captureDirectoryLoading = false;
  }

  ControlFailure get inventoryFailure => ControlFailure(
    inventoryError ?? 'error.control_result_unknown',
    inventoryErrorDiagnostic,
  );

  void _clearNetworkError() {
    networkError = null;
    networkErrorDiagnostic = null;
  }

  void _setNetworkError(Object error) {
    final failure = ControlFailure.from(error);
    networkError = failure.messageKey;
    networkErrorDiagnostic = failure.diagnostic;
  }

  void _setInventoryError(Object error) {
    final failure = ControlFailure.from(error);
    inventoryError = failure.messageKey;
    inventoryErrorDiagnostic = failure.diagnostic;
  }

  Future<void> _reconcileAccountConflict(Object error) async {
    if (error is! ControlProblem ||
        error.reasonCode != 'provider_account_conflict') {
      return;
    }
    try {
      final latest = await _api.loadDashboard();
      if (!_disposed && data != null) {
        data = _dashboardWith(
          data!,
          accounts: latest.accounts,
          endpoints: latest.endpoints,
        );
      }
    } catch (_) {
      // Preserve the failed operation and the user's draft if refresh also fails.
    }
  }

  void _setEnvironmentError(Object error) {
    final failure = switch (error) {
      EnvironmentUpstreamReferenceException() => const ControlFailure(
        'error.environment_upstream_changed',
        'environment_upstream_incompatible',
      ),
      EnvironmentAccountSelectionException() => const ControlFailure(
        'error.account_selection_empty',
        'environment_account_selection_empty',
      ),
      _ => ControlFailure.from(error),
    };
    environmentError = failure.messageKey;
    environmentErrorDiagnostic = failure.diagnostic;
  }

  String _terminalCommandError(Object error) => switch (error) {
    TerminalCommandException exception => exception.failure.copyKey,
    _ => 'terminal.error.failed',
  };

  static DashboardData _dashboardWith(
    DashboardData current, {
    RuntimeStatus? status,
    List<CaptureRecord>? captures,
    String? captureNextCursor,
    bool replaceCaptureCursor = false,
    List<EnvironmentRecord>? environments,
    List<UpstreamEndpoint>? endpoints,
    List<ProviderAccount>? accounts,
  }) => DashboardData(
    status: status ?? current.status,
    captures: captures ?? current.captures,
    captureNextCursor: replaceCaptureCursor
        ? captureNextCursor
        : current.captureNextCursor,
    environments: environments ?? current.environments,
    endpoints: endpoints ?? current.endpoints,
    accounts: accounts ?? current.accounts,
  );

  static DashboardData _mergePolledDashboard(
    DashboardData? current,
    DashboardData updated,
  ) {
    if (current == null || current.captures.length <= updated.captures.length) {
      return updated;
    }
    final captures = <String, CaptureRecord>{
      for (final capture in current.captures) capture.key: capture,
      for (final capture in updated.captures) capture.key: capture,
    };
    return DashboardData(
      status: updated.status,
      captures: captures.values.toList(growable: false),
      captureNextCursor: current.captureNextCursor,
      environments: updated.environments,
      endpoints: updated.endpoints,
      accounts: updated.accounts,
    );
  }

  static bool _sameDashboard(DashboardData? left, DashboardData right) {
    if (left == null ||
        left.captureNextCursor != right.captureNextCursor ||
        !_sameRuntimeStatus(left.status, right.status)) {
      return false;
    }
    return _sameList(left.captures, right.captures, _sameCapture) &&
        _sameList(left.environments, right.environments, _sameEnvironment) &&
        _sameList(left.endpoints, right.endpoints, _sameEndpoint) &&
        _sameList(left.accounts, right.accounts, _sameAccount);
  }

  static bool _sameRuntimeStatus(RuntimeStatus left, RuntimeStatus right) {
    final a = left.offlineHold;
    final b = right.offlineHold;
    return left.ready == right.ready &&
        left.productBuild == right.productBuild &&
        left.state == right.state &&
        left.host == right.host &&
        left.schemaRevision == right.schemaRevision &&
        left.storage == right.storage &&
        left.environmentProjection == right.environmentProjection &&
        listEquals(
          left.unavailableEnvironments,
          right.unavailableEnvironments,
        ) &&
        left.instanceId == right.instanceId &&
        left.startedAt == right.startedAt &&
        left.stoppedAt == right.stoppedAt &&
        left.stopReasonCode == right.stopReasonCode &&
        a.state == b.state &&
        a.revision == b.revision &&
        a.since == b.since &&
        a.activeActions == b.activeActions &&
        a.enteringActions == b.enteringActions &&
        a.activeEgress == b.activeEgress &&
        a.queuedRequests == b.queuedRequests &&
        a.heldBytes == b.heldBytes &&
        a.safeToDisconnect == b.safeToDisconnect &&
        mapEquals(a.activeByKind, b.activeByKind) &&
        mapEquals(a.queuedByKind, b.queuedByKind) &&
        a.lastProbeReason == b.lastProbeReason;
  }

  static bool _sameCapture(CaptureRecord left, CaptureRecord right) =>
      left.key == right.key &&
      left.displayName == right.displayName &&
      left.state == right.state &&
      left.observation == right.observation &&
      left.createdAt == right.createdAt &&
      left.updatedAt == right.updatedAt;

  static bool _sameEnvironment(
    EnvironmentRecord left,
    EnvironmentRecord right,
  ) =>
      left.id == right.id &&
      left.name == right.name &&
      left.state == right.state &&
      left.revision == right.revision &&
      left.digest == right.digest;

  static bool _sameEndpoint(UpstreamEndpoint left, UpstreamEndpoint right) =>
      left.id == right.id &&
      left.displayName == right.displayName &&
      left.origin == right.origin &&
      left.realmId == right.realmId &&
      left.state == right.state &&
      left.revision == right.revision &&
      listEquals(left.backendProtocols, right.backendProtocols) &&
      listEquals(left.capabilities, right.capabilities) &&
      listEquals(left.accountKinds, right.accountKinds);

  static bool _sameAccount(ProviderAccount left, ProviderAccount right) {
    final a = left.codexOAuth;
    final b = right.codexOAuth;
    final ta = left.tokenInfo;
    final tb = right.tokenInfo;
    return left.id == right.id &&
        left.displayName == right.displayName &&
        left.note == right.note &&
        left.noteRevision == right.noteRevision &&
        left.credentialOrigin == right.credentialOrigin &&
        listEquals(left.linkedEndpointIds, right.linkedEndpointIds) &&
        left.associationRevision == right.associationRevision &&
        left.kind == right.kind &&
        left.realmId == right.realmId &&
        left.state == right.state &&
        left.revision == right.revision &&
        left.credentialState == right.credentialState &&
        left.credentialEpoch == right.credentialEpoch &&
        listEquals(left.setHeaderNames, right.setHeaderNames) &&
        listEquals(left.deleteHeaderNames, right.deleteHeaderNames) &&
        ((a == null && b == null) ||
            (a != null &&
                b != null &&
                a.chatgptAccountId == b.chatgptAccountId &&
                a.email == b.email &&
                a.userId == b.userId &&
                a.planType == b.planType &&
                a.fedRamp == b.fedRamp &&
                a.expiresAt == b.expiresAt &&
                a.lastRefresh == b.lastRefresh &&
                a.state == b.state)) &&
        ((ta == null && tb == null) ||
            (ta != null &&
                tb != null &&
                ta.chatgptAccountId == tb.chatgptAccountId &&
                ta.email == tb.email &&
                ta.userId == tb.userId &&
                ta.planType == tb.planType &&
                ta.issuedAt == tb.issuedAt &&
                ta.authenticatedAt == tb.authenticatedAt &&
                ta.expiresAt == tb.expiresAt));
  }

  static bool _sameApprovals(
    List<ApprovalRecord> left,
    List<ApprovalRecord> right,
  ) => _sameList(
    left,
    right,
    (a, b) =>
        a.id == b.id &&
        a.revision == b.revision &&
        a.state == b.state &&
        a.requestCount == b.requestCount &&
        a.waiterCount == b.waiterCount &&
        a.expiresAt == b.expiresAt &&
        a.resolvedAt == b.resolvedAt,
  );

  static bool _sameList<T>(
    List<T> left,
    List<T> right,
    bool Function(T left, T right) same,
  ) {
    if (identical(left, right)) return true;
    if (left.length != right.length) return false;
    for (var index = 0; index < left.length; index++) {
      if (!same(left[index], right[index])) return false;
    }
    return true;
  }

  static String _newUuid() {
    final random = Random.secure();
    final bytes = List<int>.generate(16, (_) => random.nextInt(256));
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    final hex = bytes
        .map((value) => value.toRadixString(16).padLeft(2, '0'))
        .join();
    return '${hex.substring(0, 8)}-${hex.substring(8, 12)}-'
        '${hex.substring(12, 16)}-${hex.substring(16, 20)}-'
        '${hex.substring(20)}';
  }

  String newEnvironmentChildIdentityNonce() => _newUuid();

  @override
  void dispose() {
    if (_disposed) return;
    _disposed = true;
    if (_observingLifecycle) {
      WidgetsBinding.instance.removeObserver(this);
      _observingLifecycle = false;
    }
    _poller?.cancel();
    _evidencePoller?.cancel();
    _invalidateEvidenceCaches();
    unawaited(_flushPreferencesAndCloseRuntime());
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (_disposed) return;
    if (state == AppLifecycleState.hidden ||
        state == AppLifecycleState.paused ||
        state == AppLifecycleState.detached) {
      _pollingVisible = false;
    } else if (state == AppLifecycleState.resumed && !_pollingVisible) {
      _pollingVisible = true;
      unawaited(refresh());
    }
  }

  Future<void> _flushPreferencesAndCloseRuntime() async {
    try {
      await flushPreferences().timeout(const Duration(milliseconds: 750));
    } on Object {
      // Preference state is non-authoritative. Runtime shutdown must not be
      // held hostage by a stalled platform channel during application exit.
    } finally {
      await _closeRuntime();
    }
  }
}

({RawEvidenceEnvelope request, RawEvidenceEnvelope response})?
_messageTransformSamplePair(RawEvidencePage? page) {
  if (page == null) return null;
  for (final response in page.items.reversed) {
    if (response.layer != 'transform_response_input' ||
        response.payloadState != 'captured' ||
        !response.revealAvailable ||
        response.attemptId == null ||
        response.statusCode == null ||
        response.trailerCount != 0 ||
        response.redactedCredentialFields.isNotEmpty ||
        !const {
          'message_transform_input',
          'message_transform_stream_input',
        }.contains(response.representation)) {
      continue;
    }
    for (final request in page.items.reversed) {
      if (request.layer == 'transform_request_input' &&
          request.payloadState == 'captured' &&
          request.revealAvailable &&
          request.attemptId == response.attemptId &&
          request.scopeKind == response.scopeKind &&
          request.scopeId == response.scopeId &&
          request.method == 'POST' &&
          const {
            '/v1/messages',
            '/v1/responses',
            '/v1/chat/completions',
          }.contains(request.path) &&
          request.representation == 'message_transform_input' &&
          request.trailerCount == 0 &&
          request.redactedCredentialFields.isEmpty) {
        return (request: request, response: response);
      }
    }
  }
  return null;
}

final class ConversationSummary {
  const ConversationSummary({
    required this.key,
    required this.conversation,
    required this.firstObservedAt,
    required this.latest,
    required this.turnCount,
    required this.captureRunId,
  });

  factory ConversationSummary.fromRecord(ConversationRecord record) =>
      ConversationSummary(
        key: record.conversation.id,
        conversation: record.conversation,
        firstObservedAt: record.firstObservedAt,
        latest: record.latest,
        turnCount: record.turnCount,
        captureRunId: record.latest.captureRunId,
      );

  final String key;
  final ActivityConversationRef conversation;
  final DateTime firstObservedAt;
  final ActivityRecord latest;
  final int turnCount;
  final String? captureRunId;

  bool get exchangeScoped => const {
    'pending_exchange',
    'isolated_subagent',
    'isolated_exchange',
  }.contains(conversation.kind);
}

/// The number of Activities a Capture keeps in memory while it is watched.
///
/// A poll used to union its newest window into everything already held and
/// keep the result, so a Capture that kept producing Exchanges grew this list
/// without limit. The whole workbench rebuilds on every controller change, so
/// the cost of that list is paid on every notification: the UI degraded the
/// longer a live Capture stayed open, until it stopped answering clicks.
///
/// Five pages is deep enough that ordinary scrolling never reaches the edge,
/// and the tail is not lost — `loadMoreCaptureActivities` fetches it again.
const retainedCaptureActivityLimit = 500;

/// Merges a freshly polled window into what is already held.
///
/// Two rules the previous inline version got wrong. A record present in both
/// takes its **newer** value, so a status that changed between polls lands
/// instead of being overwritten by the copy already in hand. And the result is
/// bounded, oldest first, because this list is rebuilt on every notification.
List<T> mergePolledWindow<T>(
  Iterable<T> current,
  Iterable<T> latest, {
  required String Function(T) identity,
  required Comparable<Object> Function(T) recency,
  int limit = retainedCaptureActivityLimit,
}) {
  final byIdentity = <String, T>{};
  for (final item in current) {
    byIdentity[identity(item)] = item;
  }
  for (final item in latest) {
    byIdentity[identity(item)] = item;
  }
  final ordered = byIdentity.values.toList()
    ..sort((left, right) => recency(right).compareTo(recency(left)));
  if (ordered.length <= limit) return ordered;
  return ordered.sublist(0, limit);
}
