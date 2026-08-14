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
  WorkbenchController({
    required ControlApi api,
    required TerminalCommandService terminalCommands,
    required this.previewMode,
    required Future<void> Function() closeRuntime,
    WorkbenchPreferences initialPreferences = const WorkbenchPreferences(),
    WorkbenchPreferencesStore preferencesStore =
        const DiscardWorkbenchPreferencesStore(),
    bool preferencesWritable = true,
    WorkbenchPreferencesIssue? initialPreferencesIssue,
    ValueChanged<WorkbenchTheme>? onThemeChanged,
  }) : _api = api,
       _terminalCommands = terminalCommands,
       _closeRuntime = closeRuntime,
       _preferencesStore = preferencesStore,
       _preferencesWritable = preferencesWritable,
       _onThemeChanged = onThemeChanged,
       _desiredPreferences = initialPreferences,
       section = initialPreferences.section,
       language = initialPreferences.language,
       theme = initialPreferences.theme,
       selectedCaptureKey = initialPreferences.selectedCaptureKey,
       selectedConversationKey = initialPreferences.selectedConversationKey,
       selectedEnvironmentId = initialPreferences.selectedEnvironmentId,
       selectedEnvironmentRevision =
           initialPreferences.selectedEnvironmentRevision,
       selectedEndpointId = initialPreferences.selectedEndpointId,
       preferenceWarning = initialPreferencesIssue?.copyKey;

  final ControlApi _api;
  final TerminalCommandService _terminalCommands;
  final Future<void> Function() _closeRuntime;
  final WorkbenchPreferencesStore _preferencesStore;
  final bool _preferencesWritable;
  final ValueChanged<WorkbenchTheme>? _onThemeChanged;
  final bool previewMode;

  DashboardData? data;
  NetworkData? networkData;
  List<ApprovalRecord>? pendingApprovals;
  ConversationPage? conversationIndex;
  ActivityPage? selectedConversationPage;
  ConversationPage? selectedCaptureConversations;
  ActivityPage? selectedCapturePage;
  EnvironmentDraft? reviewedEnvironmentDraft;
  EnvironmentImpact? reviewedEnvironmentImpact;
  EnvironmentRecord? historicalEnvironment;
  CaptureAssignment? selectedAssignment;
  WorkspaceEnvironmentDefault? selectedWorkspaceDefault;
  TerminalCommandStatus? terminalCommand;
  WorkbenchSection section;
  AppLanguage language;
  WorkbenchTheme theme;
  String? selectedCaptureKey;
  String? selectedCaptureConversationKey;
  String? selectedConversationKey;
  String? selectedEnvironmentId;
  int? selectedEnvironmentRevision;
  String? selectedEndpointId;
  String? preferenceWarning;
  String? errorMessage;
  String? operationNotice;
  String? networkError;
  String? conversationsError;
  String? captureDirectoryError;
  String? networkNotice;
  String? inventoryError;
  String? inventoryNotice;
  String? environmentError;
  String? environmentNotice;
  String? offlineError;
  String? offlineNotice;
  String? workspaceDefaultError;
  String? workspaceDefaultNotice;
  String? terminalCommandError;
  String? terminalCommandNotice;
  String? approvalAttentionError;
  bool loading = true;
  bool detailLoading = false;
  bool mutating = false;
  bool networkLoading = false;
  bool conversationsLoading = false;
  bool captureDirectoryLoading = false;
  bool captureActivitiesLoading = false;
  bool networkMutating = false;
  bool inventoryMutating = false;
  bool environmentMutating = false;
  bool environmentRevisionLoading = false;
  bool offlineMutating = false;
  bool workspaceDefaultLoading = false;
  bool workspaceDefaultMutating = false;
  bool terminalCommandLoading = false;
  bool terminalCommandMutating = false;
  bool pendingApprovalsLoading = false;
  int _selectionGeneration = 0;
  int _conversationGeneration = 0;
  int _environmentRevisionGeneration = 0;
  final Map<String, ExchangeDetail> _exchangeDetails = {};
  final Set<String> _loadingExchanges = {};
  final LinkedHashMap<String, RawEvidencePage> _rawEvidencePages =
      LinkedHashMap<String, RawEvidencePage>();
  final Set<String> _loadingRawEvidence = {};
  final Map<String, String> _rawEvidenceErrors = {};
  Timer? _poller;
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

  OfflineHoldSnapshot? get offlineHold => data?.status.offlineHold;

  int? get pendingApprovalCount => pendingApprovals?.length;

  List<ConversationSummary> get conversations =>
      conversationIndex?.items
          .map(ConversationSummary.fromRecord)
          .toList(growable: false) ??
      const [];

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

  ConversationSummary? get selectedConversation {
    final key = selectedConversationKey;
    if (key == null) return null;
    return conversations.where((value) => value.key == key).firstOrNull;
  }

  ExchangeDetail? exchangeDetail(
    String exchangeId, {
    String contentView = 'incremental',
  }) => _exchangeDetails['$exchangeId:$contentView'];

  bool exchangeIsLoading(
    String exchangeId, {
    String contentView = 'incremental',
  }) => _loadingExchanges.contains('$exchangeId:$contentView');

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
    if (_disposed) return;
    if (data == null) loading = true;
    errorMessage = null;
    notifyListeners();
    try {
      final updated = await _api.loadDashboard();
      if (_disposed) return;
      data = updated;
      if (selectDefaults || selectedCapture == null) {
        final initial =
            runningCaptures.firstOrNull ?? historicalCaptures.firstOrNull;
        selectedCaptureKey = initial?.key;
      }
      if (selectedEnvironmentRevision == null &&
          !updated.environments.any(
            (environment) => environment.id == selectedEnvironmentId,
          )) {
        selectedEnvironmentId = updated.environments.firstOrNull?.id;
      }
      if (!updated.endpoints.any(
        (endpoint) => endpoint.id == selectedEndpointId,
      )) {
        selectedEndpointId = updated.endpoints.firstOrNull?.id;
      }
      loading = false;
      notifyListeners();
      if (selectedCapture != null) await _loadCaptureDetail(selectedCapture!);
      if (section == WorkbenchSection.network) {
        await _refreshNetwork();
      } else {
        await refreshPendingApprovals();
      }
      if (section == WorkbenchSection.conversations) {
        await _refreshConversations();
      }
      if (section == WorkbenchSection.settings) {
        await refreshTerminalCommand();
      }
    } catch (error) {
      if (_disposed) return;
      loading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> _poll() async {
    if (_disposed ||
        loading ||
        captureDirectoryLoading ||
        mutating ||
        networkMutating ||
        environmentMutating ||
        workspaceDefaultMutating) {
      return;
    }
    try {
      final updated = await _api.loadDashboard();
      if (_disposed) return;
      data = _mergePolledDashboard(data, updated);
      notifyListeners();
      if (section != WorkbenchSection.network) {
        await refreshPendingApprovals(quiet: true);
      }
      final capture = selectedCapture;
      if (capture != null) await _loadCaptureDetail(capture, quiet: true);
      if (section == WorkbenchSection.network) {
        await _refreshNetwork(quiet: true);
      }
      if (section == WorkbenchSection.conversations) {
        await _refreshConversations(quiet: true);
      }
      if (section == WorkbenchSection.settings) {
        await refreshTerminalCommand(quiet: true);
      }
    } catch (_) {
      // A transient poll must not replace useful evidence with an error page.
      // Explicit refresh still surfaces the exact failure.
    }
  }

  Future<void> loadMoreCaptures() async {
    final current = data;
    final cursor = current?.captureNextCursor;
    if (_disposed ||
        loading ||
        current == null ||
        cursor == null ||
        captureDirectoryLoading) {
      return;
    }
    captureDirectoryLoading = true;
    captureDirectoryError = null;
    notifyListeners();
    try {
      final page = await _api.captures(cursor: cursor);
      if (_disposed) return;
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
    if (section == value) return;
    section = value;
    operationNotice = null;
    notifyListeners();
    if (value == WorkbenchSection.network && networkData == null) {
      unawaited(_refreshNetwork());
    }
    if (value == WorkbenchSection.conversations && conversationIndex == null) {
      unawaited(_refreshConversations());
    }
    if (value == WorkbenchSection.settings && terminalCommand == null) {
      unawaited(refreshTerminalCommand());
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

  Future<void> refreshConversations() => _refreshConversations();

  Future<void> _refreshConversations({bool quiet = false}) async {
    if (_disposed || conversationsLoading) return;
    if (!quiet) {
      conversationsLoading = true;
      conversationsError = null;
      notifyListeners();
    }
    try {
      final updated = await _api.conversations(limit: 50);
      if (_disposed) return;
      final previouslySelected = selectedConversation;
      conversationIndex = updated;
      final available = conversations;
      final selectionStillExists = available.any(
        (value) => value.key == selectedConversationKey,
      );
      if (!selectionStillExists) {
        // A live Exchange is deliberately isolated until terminal protocol
        // evidence can prove its final Conversation. Preserve the selection
        // across that projection change by the immutable Exchange identity.
        final migrated =
            previouslySelected?.conversation.kind == 'pending_exchange'
            ? available
                  .where(
                    (value) => value.latest.id == previouslySelected!.latest.id,
                  )
                  .firstOrNull
            : null;
        selectedConversationKey = migrated?.key ?? available.firstOrNull?.key;
        selectedConversationPage = null;
      }
      conversationsLoading = false;
      notifyListeners();
      if (!quiet && selectedConversationPage == null) {
        final selected = selectedConversation;
        if (selected != null) await _loadConversation(selected);
      }
    } catch (error) {
      if (_disposed || quiet) return;
      conversationsLoading = false;
      conversationsError = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreConversations() async {
    final current = conversationIndex;
    final cursor = current?.nextCursor;
    if (current == null || cursor == null || conversationsLoading) return;
    conversationsLoading = true;
    conversationsError = null;
    notifyListeners();
    try {
      final page = await _api.conversations(cursor: cursor, limit: 50);
      if (_disposed) return;
      final unique = <String, ConversationRecord>{
        for (final item in current.items) item.conversation.id: item,
        for (final item in page.items) item.conversation.id: item,
      };
      conversationIndex = ConversationPage(
        items: unique.values.toList(growable: false),
        nextCursor: page.nextCursor,
      );
      conversationsLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed) return;
      conversationsLoading = false;
      conversationsError = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> selectConversation(String key) async {
    if (selectedConversationKey == key && selectedConversationPage != null) {
      return;
    }
    selectedConversationKey = key;
    selectedConversationPage = null;
    conversationsError = null;
    notifyListeners();
    final selected = selectedConversation;
    if (selected != null) await _loadConversation(selected);
  }

  Future<void> openSelectedConversationCapture() async {
    final activity = selectedConversation?.latest;
    if (activity == null) return;
    final key = switch ((activity.captureRunId, activity.manualCaptureId)) {
      (final id?, _) => 'managed_run:$id',
      (_, final id?) => 'manual_capture:$id',
      _ => null,
    };
    if (key == null ||
        data?.captures.any((capture) => capture.key == key) != true) {
      return;
    }
    await selectCapture(key);
    if (_disposed) return;
    selectSection(WorkbenchSection.captures);
  }

  Future<void> _loadConversation(ConversationSummary conversation) async {
    final generation = ++_conversationGeneration;
    conversationsLoading = true;
    conversationsError = null;
    notifyListeners();
    try {
      final page = await _api.activities(
        limit: 200,
        conversationId: conversation.key,
      );
      if (_disposed ||
          generation != _conversationGeneration ||
          selectedConversationKey != conversation.key) {
        return;
      }
      selectedConversationPage =
          page.items.isEmpty && conversation.latest.status == 'pending'
          ? ActivityPage(items: [conversation.latest], nextCursor: null)
          : page;
      conversationsLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || generation != _conversationGeneration) return;
      conversationsLoading = false;
      conversationsError = _describeError(error);
      notifyListeners();
    }
  }

  Future<void> loadMoreSelectedConversation() async {
    final selected = selectedConversation;
    final current = selectedConversationPage;
    final cursor = current?.nextCursor;
    if (selected == null ||
        current == null ||
        cursor == null ||
        conversationsLoading) {
      return;
    }
    conversationsLoading = true;
    conversationsError = null;
    notifyListeners();
    try {
      final page = await _api.activities(
        cursor: cursor,
        limit: 200,
        conversationId: selected.key,
      );
      if (_disposed) return;
      final unique = <String, ActivityRecord>{
        for (final item in current.items) item.id: item,
        for (final item in page.items) item.id: item,
      };
      selectedConversationPage = ActivityPage(
        items: unique.values.toList(growable: false),
        nextCursor: page.nextCursor,
      );
      conversationsLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed) return;
      conversationsLoading = false;
      conversationsError = _describeError(error);
      notifyListeners();
    }
  }

  Future<ExchangeDetail?> loadExchangeDetail(
    String exchangeId, {
    String contentView = 'incremental',
  }) async {
    final key = '$exchangeId:$contentView';
    final cached = _exchangeDetails[key];
    if (cached != null) return cached;
    if (_loadingExchanges.contains(key)) return null;
    _loadingExchanges.add(key);
    conversationsError = null;
    notifyListeners();
    try {
      final detail = await _api.exchange(exchangeId, contentView: contentView);
      if (_disposed) return null;
      _exchangeDetails[key] = detail;
      _loadingExchanges.remove(key);
      notifyListeners();
      return detail;
    } catch (error) {
      if (_disposed) return null;
      _loadingExchanges.remove(key);
      conversationsError = _describeError(error);
      notifyListeners();
      return null;
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
    _loadingRawEvidence.add(exchangeId);
    _rawEvidenceErrors.remove(exchangeId);
    notifyListeners();
    try {
      final page = await _api.rawEvidence(exchangeId);
      if (_disposed) return null;
      _rawEvidencePages.remove(exchangeId);
      _rawEvidencePages[exchangeId] = page;
      while (_rawEvidencePages.length > 64) {
        _rawEvidencePages.remove(_rawEvidencePages.keys.first);
      }
      _loadingRawEvidence.remove(exchangeId);
      notifyListeners();
      return page;
    } catch (error) {
      if (_disposed) return null;
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
    required String kind,
  }) async {
    final current = data;
    if (current == null || inventoryMutating) return null;
    inventoryMutating = true;
    inventoryError = null;
    inventoryNotice = null;
    notifyListeners();
    try {
      final provider = kind == 'anthropic' ? 'anthropic' : 'openai';
      final created = await _api.createUpstreamEndpoint(
        id: 'target.custom.$provider.${_newUuid()}',
        displayName: displayName.trim(),
        origin: origin.trim(),
        kind: kind,
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
        'claude_oauth_token' => 'claude',
        'openai_api_key' => 'openai',
        _ => 'anthropic',
      };
      final created = await _api.createProviderAccount(
        id: 'account.$provider.${_newUuid()}',
        displayName: displayName.trim(),
        upstreamEndpointId: endpoint.id,
        kind: kind,
        secret: secret,
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
    selectedAssignment = null;
    selectedWorkspaceDefault = null;
    selectedCaptureConversations = null;
    selectedCaptureConversationKey = null;
    selectedCapturePage = null;
    captureActivitiesLoading = false;
    workspaceDefaultLoading = false;
    workspaceDefaultError = null;
    workspaceDefaultNotice = null;
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
    selectedConversationKey: selectedConversationKey,
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
    final currentWorkspaceDefault = selectedWorkspaceDefault;
    if (!quiet) {
      detailLoading = true;
      workspaceDefaultLoading =
          capture.managedRun?.hasWorkspaceIdentity == true;
      errorMessage = null;
      notifyListeners();
    }
    try {
      final values = await Future.wait<Object?>([
        _api.captureAssignment(capture.key),
        _captureConversationPage(capture, limit: 200),
        _workspaceDefaultFor(capture),
      ]);
      if (_disposed ||
          generation != _selectionGeneration ||
          selectedCaptureKey != capture.key) {
        return;
      }
      selectedAssignment = values[0]! as CaptureAssignment;
      final conversationPage = values[1]! as ConversationPage;
      final workspaceLoad = values[2]! as _WorkspaceDefaultLoad;
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
      if (quiet &&
          currentPage != null &&
          currentConversationKey == selectedCaptureConversationKey) {
        final unique = <String, ActivityRecord>{
          for (final item in latest.items) item.id: item,
          for (final item in currentPage.items) item.id: item,
        };
        selectedCapturePage = ActivityPage(
          items: unique.values.toList(growable: false),
          nextCursor: currentPage.nextCursor,
        );
      } else {
        selectedCapturePage = latest;
      }
      if (workspaceLoad.error == null) {
        selectedWorkspaceDefault = workspaceLoad.value;
        workspaceDefaultError = null;
      } else if (!quiet) {
        selectedWorkspaceDefault = null;
        workspaceDefaultError = _describeError(workspaceLoad.error!);
      } else {
        selectedWorkspaceDefault = currentWorkspaceDefault;
      }
      workspaceDefaultLoading = false;
      captureActivitiesLoading = false;
      detailLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || generation != _selectionGeneration || quiet) return;
      captureActivitiesLoading = false;
      detailLoading = false;
      workspaceDefaultLoading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    }
  }

  Future<_WorkspaceDefaultLoad> _workspaceDefaultFor(
    CaptureRecord capture,
  ) async {
    final managed = capture.managedRun;
    final machineId = managed?.machineId;
    final workspaceId = managed?.workspaceId;
    if (machineId == null || workspaceId == null) {
      return const _WorkspaceDefaultLoad(value: null);
    }
    try {
      return _WorkspaceDefaultLoad(
        value: await _api.workspaceEnvironmentDefault(
          machineId: machineId,
          workspaceId: workspaceId,
        ),
      );
    } catch (error) {
      return _WorkspaceDefaultLoad(value: null, error: error);
    }
  }

  Future<ActivityPage> _captureActivityPage(
    CaptureRecord capture, {
    String? cursor,
    String? conversationId,
    required int limit,
  }) => _api.activities(
    cursor: cursor,
    limit: limit,
    captureRunId: capture.isManual ? null : capture.captureRunId,
    manualCaptureId: capture.isManual ? capture.id : null,
    conversationId: conversationId,
  );

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
    selectedCapturePage = null;
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
      selectedCapturePage = page;
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
      if (_disposed || selectedCaptureKey != captureKey) return;
      final unique = <String, ActivityRecord>{
        for (final item in current.items) item.id: item,
        for (final item in page.items) item.id: item,
      };
      selectedCapturePage = ActivityPage(
        items: unique.values.toList(growable: false),
        nextCursor: page.nextCursor,
      );
      captureActivitiesLoading = false;
      notifyListeners();
    } catch (error) {
      if (_disposed || selectedCaptureKey != captureKey) return;
      captureActivitiesLoading = false;
      errorMessage = _describeError(error);
      notifyListeners();
    }
  }

  Future<CaptureAssignmentChange?> switchEnvironment(
    String environmentId,
  ) async {
    final assignment = selectedAssignment;
    if (assignment == null || mutating) return null;
    mutating = true;
    operationNotice = null;
    errorMessage = null;
    notifyListeners();
    try {
      final change = await _api.switchCaptureEnvironment(
        assignment: assignment,
        environmentId: environmentId,
      );
      if (_disposed) return null;
      selectedAssignment = change.assignment;
      operationNotice = change.applied
          ? 'environment.${change.boundary}'
          : 'environment.${change.reasonCode ?? change.boundary}';
      mutating = false;
      notifyListeners();
      return change;
    } catch (error) {
      if (_disposed) return null;
      errorMessage = _describeError(error);
      mutating = false;
      notifyListeners();
      return null;
    }
  }

  Future<bool> setSelectedWorkspaceDefault(String? environmentId) async {
    final capture = selectedCapture;
    final managed = capture?.managedRun;
    final machineId = managed?.machineId;
    final workspaceId = managed?.workspaceId;
    final current = selectedWorkspaceDefault;
    if (capture == null ||
        machineId == null ||
        workspaceId == null ||
        workspaceDefaultMutating ||
        workspaceDefaultLoading) {
      return false;
    }
    if (environmentId == current?.environmentId ||
        (environmentId == null && current == null)) {
      return true;
    }
    if (environmentId != null) {
      final candidate = data?.environments
          .where(
            (environment) =>
                environment.id == environmentId &&
                environment.state == 'active' &&
                !environment.systemOwned,
          )
          .firstOrNull;
      if (candidate == null) return false;
    }
    final captureKey = capture.key;
    workspaceDefaultMutating = true;
    workspaceDefaultError = null;
    workspaceDefaultNotice = null;
    notifyListeners();
    try {
      if (environmentId == null) {
        await _api.clearWorkspaceEnvironmentDefault(current: current!);
      } else {
        final updated = await _api.setWorkspaceEnvironmentDefault(
          machineId: machineId,
          workspaceId: workspaceId,
          expectedRevision: current?.revision ?? 0,
          environmentId: environmentId,
        );
        if (_disposed || selectedCaptureKey != captureKey) return false;
        selectedWorkspaceDefault = updated;
      }
      if (_disposed || selectedCaptureKey != captureKey) return false;
      if (environmentId == null) selectedWorkspaceDefault = null;
      workspaceDefaultMutating = false;
      workspaceDefaultNotice = environmentId == null
          ? 'workspace_default.cleared'
          : 'workspace_default.saved';
      notifyListeners();
      return true;
    } catch (error) {
      if (_disposed || selectedCaptureKey != captureKey) return false;
      workspaceDefaultError = _describeError(error);
      await _reconcileWorkspaceDefault(
        machineId: machineId,
        workspaceId: workspaceId,
        captureKey: captureKey,
      );
      if (_disposed || selectedCaptureKey != captureKey) return false;
      workspaceDefaultMutating = false;
      notifyListeners();
      return false;
    }
  }

  Future<void> _reconcileWorkspaceDefault({
    required String machineId,
    required String workspaceId,
    required String captureKey,
  }) async {
    try {
      final latest = await _api.workspaceEnvironmentDefault(
        machineId: machineId,
        workspaceId: workspaceId,
      );
      if (!_disposed && selectedCaptureKey == captureKey) {
        selectedWorkspaceDefault = latest;
      }
    } catch (_) {
      // Keep the original mutation failure. Reconciliation is best-effort and
      // must not disguise the CAS error that explains why the write failed.
    }
  }

  void clearWorkspaceDefaultNotice() {
    workspaceDefaultError = null;
    workspaceDefaultNotice = null;
    notifyListeners();
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

final class _WorkspaceDefaultLoad {
  const _WorkspaceDefaultLoad({required this.value, this.error});

  final WorkspaceEnvironmentDefault? value;
  final Object? error;
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
