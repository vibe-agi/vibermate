import 'dart:async';
import 'dart:collection';
import 'dart:math';

import 'package:flutter/foundation.dart';

import '../../core/api/control_api.dart';
import '../../core/api/control_models.dart';
import '../../core/bootstrap/terminal_command.dart';
import '../../core/preferences/workbench_preferences.dart';

export '../../core/preferences/workbench_preferences.dart'
    show AppLanguage, WorkbenchSection, WorkbenchTheme;

final class WorkbenchController extends ChangeNotifier {
  static const _exchangeDetailCacheLimit = 64;
  static const _fullExchangeDetailCacheLimit = 2;
  static const _captureConversationPageCacheLimit = 24;

  WorkbenchController({
    required ControlApi api,
    required TerminalCommandService terminalCommands,
    required this.previewMode,
    required Future<void> Function() closeRuntime,
    this.serverManagement = false,
    this.terminalManagement = true,
    this.runtimeTarget = 'This Mac',
    WorkbenchPreferences initialPreferences = const WorkbenchPreferences(),
    WorkbenchPreferencesStore preferencesStore =
        const DiscardWorkbenchPreferencesStore(),
    bool preferencesWritable = true,
    WorkbenchPreferencesIssue? initialPreferencesIssue,
    ValueChanged<WorkbenchTheme>? onThemeChanged,
    DateTime Function()? clock,
  }) : _api = api,
       _terminalCommands = terminalCommands,
       _closeRuntime = closeRuntime,
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
  final TerminalCommandService _terminalCommands;
  final Future<void> Function() _closeRuntime;
  final DateTime Function() _clock;
  final WorkbenchPreferencesStore _preferencesStore;
  final bool _preferencesWritable;
  final ValueChanged<WorkbenchTheme>? _onThemeChanged;
  final bool previewMode;
  final bool serverManagement;
  final bool terminalManagement;
  final String runtimeTarget;

  String get runtimeConnectTarget =>
      serverAccess?.preferredTarget ?? runtimeTarget;

  DashboardData? data;
  NetworkData? networkData;
  List<ApprovalRecord>? pendingApprovals;
  ConversationPage? selectedCaptureConversations;
  ActivityPage? selectedCapturePage;
  EnvironmentDraft? reviewedEnvironmentDraft;
  EnvironmentImpact? reviewedEnvironmentImpact;
  EnvironmentRecord? historicalEnvironment;
  CaptureAssignment? selectedAssignment;
  TerminalCommandStatus? terminalCommand;
  RuntimeServerAccess? serverAccess;
  List<RuntimeUser>? runtimeUsers;
  RuntimeUsageReport? runtimeUsage;
  CapturedMessageTransformSample? capturedMessageTransformSample;
  int usageRangeDays = 30;
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
  String? captureDirectoryError;
  String? networkNotice;
  String? inventoryError;
  String? inventoryNotice;
  String? environmentError;
  String? environmentNotice;
  String? offlineError;
  String? offlineNotice;
  String? terminalCommandError;
  String? terminalCommandNotice;
  String? serverManagementError;
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
  bool offlineMutating = false;
  bool terminalCommandLoading = false;
  bool terminalCommandMutating = false;
  bool serverManagementLoading = false;
  bool runtimeUserMutating = false;
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
  int _rawEvidenceGeneration = 0;
  Timer? _poller;
  bool _pollInFlight = false;
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
    if (!refresh) {
      final cached = _upstreamModelCatalogs[key];
      if (cached != null) return cached;
    }
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

  Future<CodeLibraryCatalog> codeLibrary() => _api.codeLibrary();

  Future<CodeLibraryCollection> createCodeLibraryCollection({
    required String displayName,
  }) => _api.createCodeLibraryCollection(
    id: 'collection.custom.${_newUuid()}',
    displayName: displayName,
  );

  Future<CodeLibraryTransformRevision> createCodeLibraryTransform({
    required String collectionId,
    required String displayName,
    required TrafficTransformPolicy policy,
  }) => _api.publishCodeLibraryTransform(
    id: 'transform.custom.${_newUuid()}',
    expectedRevision: 0,
    collectionId: collectionId,
    displayName: displayName,
    policy: policy,
  );

  Future<CodeLibraryTransformRevision> publishCodeLibraryTransform({
    required String id,
    required int expectedRevision,
    required String collectionId,
    required String displayName,
    required TrafficTransformPolicy policy,
  }) => _api.publishCodeLibraryTransform(
    id: id,
    expectedRevision: expectedRevision,
    collectionId: collectionId,
    displayName: displayName,
    policy: policy,
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

  OfflineHoldSnapshot? get offlineHold => data?.status.offlineHold;

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
    await refresh();
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
  }

  Future<void> refresh({bool selectDefaults = false}) async {
    if (_disposed || inventoryMutating) return;
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
      data = _mergePolledDashboard(data, updated);
      _repairDashboardSelections(data!);
      notifyListeners();
      if (section != WorkbenchSection.network) {
        await refreshPendingApprovals(quiet: true);
      }
      final capture = selectedCapture;
      if (capture != null) await _loadCaptureDetail(capture, quiet: true);
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

  Future<void> loadMoreCaptures() async {
    final current = data;
    final cursor = current?.captureNextCursor;
    if (_disposed ||
        loading ||
        inventoryMutating ||
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

  Future<void> selectUsageRangeDays(int days) async {
    if (_disposed ||
        !serverManagement ||
        serverManagementLoading ||
        !const {30, 90, 365}.contains(days) ||
        (usageRangeDays == days && runtimeUsage != null)) {
      return;
    }
    final previousDays = usageRangeDays;
    usageRangeDays = days;
    serverManagementLoading = true;
    serverManagementError = null;
    notifyListeners();
    try {
      final report = await _api.runtimeUsage(_usageQuery());
      if (_disposed) return;
      runtimeUsage = report;
      serverManagementLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed) return;
      usageRangeDays = previousDays;
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

  Future<bool> disableRuntimeUser(String userId) async {
    if (_disposed || !serverManagement || runtimeUserMutating) return false;
    runtimeUserMutating = true;
    serverManagementError = null;
    notifyListeners();
    try {
      final disabled = await _api.disableRuntimeUser(userId);
      if (_disposed) return false;
      runtimeUsers = List<RuntimeUser>.unmodifiable([
        for (final user in runtimeUsers ?? const <RuntimeUser>[])
          if (user.id == userId) disabled else user,
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
      pendingApprovals = updated;
      approvalAttentionError = null;
      pendingApprovalsLoading = false;
      notifyListeners();
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

  Future<bool> enterOfflineHold() => _changeOfflineHold(resume: false);

  Future<bool> resumeOfflineHold() => _changeOfflineHold(resume: true);

  Future<bool> _changeOfflineHold({required bool resume}) async {
    final current = offlineHold;
    if (_disposed ||
        offlineMutating ||
        current == null ||
        (resume ? !current.canResume : !current.canEnter)) {
      return false;
    }
    offlineMutating = true;
    offlineError = null;
    offlineNotice = null;
    notifyListeners();
    try {
      final updated = resume
          ? await _api.resumeOfflineHold(current)
          : await _api.enterOfflineHold(current);
      if (_disposed) return false;
      final dashboard = data;
      if (dashboard != null) {
        data = _dashboardWith(
          dashboard,
          status: dashboard.status.withOfflineHold(updated),
        );
      }
      if (resume && updated.state == 'held') {
        offlineError = updated.lastProbeReason ?? 'probe_failed';
        offlineMutating = false;
        notifyListeners();
        return false;
      }
      offlineNotice = resume
          ? updated.state == 'online'
                ? 'offline.resumed'
                : 'offline.releasing'
          : 'offline.held';
      offlineMutating = false;
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed) return false;
      final message = _describeError(error);
      try {
        final refreshed = await _api.loadDashboard();
        if (!_disposed) data = refreshed;
      } catch (_) {
        // The original mutation error is the useful authority. A failed
        // reconciliation must not replace it with a generic refresh failure.
      }
      if (_disposed) return false;
      offlineError = message;
      offlineMutating = false;
      notifyListeners();
      return false;
    }
  }

  void clearOfflineMessage() {
    offlineError = null;
    offlineNotice = null;
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
        revealed.body.fillRange(0, revealed.body.length, 0);
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

  Future<bool> copyMessageTransformSample(String exchangeId) async {
    final page = await loadRawEvidence(exchangeId);
    if (page == null) return false;
    final pair = _messageTransformSamplePair(page);
    if (pair == null) {
      _rawEvidenceErrors[exchangeId] = 'message_transform_sample_unavailable';
      notifyListeners();
      return false;
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
      capturedMessageTransformSample =
          CapturedMessageTransformSample.fromRawEvidence(
            request: requestReveal,
            response: responseReveal,
          );
      _rawEvidenceErrors.remove(exchangeId);
      section = WorkbenchSection.codeLibrary;
      notifyListeners();
      return true;
    } on Object catch (error) {
      _rawEvidenceErrors[exchangeId] = _describeError(error);
      notifyListeners();
      return false;
    } finally {
      requestReveal?.body.fillRange(0, requestReveal.body.length, 0);
      responseReveal?.body.fillRange(0, responseReveal.body.length, 0);
    }
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
      networkError = null;
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
      networkError = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreConnections() async {
    final current = networkData;
    final cursor = current?.connections.nextCursor;
    if (current == null || cursor == null || networkLoading) return;
    networkLoading = true;
    networkError = null;
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
      networkError = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreEgressAttempts() async {
    final current = networkData;
    final cursor = current?.egressAttempts.nextCursor;
    if (current == null || cursor == null || networkLoading) return;
    networkLoading = true;
    networkError = null;
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
      networkError = _describeError(error);
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
    networkError = null;
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
      networkError = _describeError(error);
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
    networkError = null;
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
      networkError = _describeError(error);
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
    if (current == null || inventoryMutating) return null;
    inventoryMutating = true;
    inventoryError = null;
    inventoryNotice = null;
    notifyListeners();
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
      inventoryError = _describeError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ProviderAccount?> createProviderAccount({
    required UpstreamEndpoint endpoint,
    required String displayName,
    required String kind,
    required String secret,
    required ProviderAccountHeaderPolicy headerPolicy,
  }) async {
    final current = data;
    if (current == null ||
        inventoryMutating ||
        !endpoint.accountKinds.contains(kind)) {
      return null;
    }
    inventoryMutating = true;
    inventoryError = null;
    inventoryNotice = null;
    notifyListeners();
    try {
      final provider = switch (kind) {
        'anthropic_api_key' => 'anthropic',
        'bearer_token' => 'bearer',
        _ => 'account',
      };
      final created = await _api.createProviderAccount(
        id: 'account.$provider.${_newUuid()}',
        displayName: displayName.trim(),
        upstreamEndpointId: endpoint.id,
        kind: kind,
        secret: secret,
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
      inventoryError = _describeError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ProviderAccount?> replaceProviderAccountCredential({
    required ProviderAccount account,
    required String secret,
    required ProviderAccountHeaderPolicy headerPolicy,
  }) async {
    final current = data;
    if (current == null || inventoryMutating) return null;
    inventoryMutating = true;
    inventoryError = null;
    inventoryNotice = null;
    notifyListeners();
    try {
      final updated = await _api.replaceProviderAccountCredential(
        account: account,
        secret: secret,
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
      inventoryError = _describeError(error);
      notifyListeners();
      return null;
    }
  }

  Future<ProviderAccountDeleteResult?> deleteProviderAccount(
    ProviderAccount account,
  ) async {
    final current = data;
    if (current == null || inventoryMutating) return null;
    inventoryMutating = true;
    inventoryError = null;
    inventoryNotice = null;
    notifyListeners();
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
      inventoryError = _describeError(error);
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
    if (current == null || inventoryMutating) return null;
    final generation = ++_dashboardGeneration;
    captureDirectoryLoading = false;
    inventoryMutating = true;
    inventoryError = null;
    inventoryNotice = null;
    notifyListeners();
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
          inventoryError = _describeError(error);
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
      inventoryError = _describeError(error);
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
      environmentError = _describeError(error);
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
    EnvironmentDraftInput candidate,
  ) async {
    final environment = selectedEnvironment;
    if (environment == null ||
        selectedEnvironmentRevision != null ||
        environment.systemOwned ||
        environmentMutating) {
      return null;
    }
    return _reviewEnvironmentDraft(
      environmentId: environment.id,
      expectedBaseRevision: environment.revision,
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
    candidate: candidate,
    requireCurrentSelection: false,
  );

  Future<EnvironmentImpact?> _reviewEnvironmentDraft({
    required String environmentId,
    required int expectedBaseRevision,
    required EnvironmentDraftInput candidate,
    required bool requireCurrentSelection,
  }) async {
    if (environmentMutating) return null;
    environmentMutating = true;
    environmentError = null;
    environmentNotice = null;
    reviewedEnvironmentDraft = null;
    reviewedEnvironmentImpact = null;
    notifyListeners();
    try {
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
        input: candidate.withExpectedDraftRevision(expectedDraftRevision),
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
      environmentError = _describeError(error);
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
    environmentMutating = true;
    environmentError = null;
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
      environmentError = _describeError(error);
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
  }) async {
    if (quiet && captureActivitiesLoading) return;
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
        _api.captureAssignment(capture.key),
        _captureConversationPage(capture, limit: 200),
      ]);
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != capture.key) {
        return;
      }
      selectedAssignment = values[0]! as CaptureAssignment;
      final conversationPage = values[1]! as ConversationPage;
      selectedCaptureConversations = conversationPage;
      final available = captureConversations;
      if (!available.any(
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
      ControlProblem problem => '${problem.reasonCode} (${problem.status})',
      ControlContractException contract => contract.message,
      _ => error.toString(),
    };
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
    _poller?.cancel();
    unawaited(_flushPreferencesAndCloseRuntime());
    super.dispose();
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
