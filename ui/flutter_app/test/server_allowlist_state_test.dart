import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

final class _DelayedAllowlistApi extends Fake implements ControlApi {
  final preview = PreviewControlApi();
  final entered = Completer<void>();
  final reads = <Completer<ServerIpAllowlist>>[];
  Object? saveError;
  ServerIpAllowlist authoritative = const ServerIpAllowlist(
    revision: 1,
    ranges: [],
    maxRanges: 256,
  );

  @override
  Future<RuntimeServerAccess> serverAccess() => preview.serverAccess();
  @override
  Future<List<RuntimeUser>> runtimeUsers() => preview.runtimeUsers();
  @override
  Future<ServerIpAllowlist> serverIpAllowlist() {
    if (!entered.isCompleted) entered.complete();
    final read = Completer<ServerIpAllowlist>();
    reads.add(read);
    return read.future;
  }

  @override
  Future<ServerIpAllowlist> replaceServerIpAllowlist({
    required int revision,
    required List<String> ranges,
  }) async {
    if (saveError case final error?) throw error;
    expect(revision, authoritative.revision);
    return authoritative = ServerIpAllowlist(
      revision: revision + 1,
      ranges: ranges,
      maxRanges: 256,
    );
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  test(
    'late allowlist GET must not replace an acknowledged PATCH result',
    () async {
      final api = _DelayedAllowlistApi();
      final controller = WorkbenchController(
        api: api,
        terminalCommands: PreviewTerminalCommandService(),
        previewMode: true,
        serverManagement: true,
        terminalManagement: false,
        closeRuntime: api.preview.close,
        initialPreferences: const WorkbenchPreferences(
          section: WorkbenchSection.settings,
        ),
      );
      addTearDown(controller.dispose);
      addTearDown(api.preview.close);
      final stale = api.authoritative;
      controller.serverIpAllowlist = stale;
      final oldRefresh = controller.refreshServerManagement(quiet: true);
      await api.entered.future.timeout(const Duration(seconds: 2));
      expect(
        await controller.saveServerIpAllowlist(['203.0.113.0/24']),
        isNull,
      );
      expect(controller.serverIpAllowlist!.revision, 2);
      api.reads.single.complete(stale);
      await oldRefresh.timeout(const Duration(seconds: 2));
      expect(
        controller.serverIpAllowlist!.revision,
        api.authoritative.revision,
      );
      expect(controller.serverIpAllowlist!.ranges, api.authoritative.ranges);
    },
  );
  test(
    'later quiet allowlist read owns publication when reads finish out of order',
    () async {
      final api = _DelayedAllowlistApi();
      final controller = _makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.preview.close);
      final old = controller.refreshServerManagement(quiet: true);
      await _waitForReads(api, 1);
      final newer = controller.refreshServerManagement(quiet: true);
      await _waitForReads(api, 2);
      const current = ServerIpAllowlist(
        revision: 3,
        ranges: ['198.51.100.0/24'],
        maxRanges: 256,
      );
      api.reads[1].complete(current);
      await newer;
      api.reads[0].complete(api.authoritative);
      await old;
      expect(controller.serverIpAllowlist, same(current));
    },
  );

  test(
    'allowlist conflict reacquires authoritative list before retry',
    () async {
      final api = _DelayedAllowlistApi();
      final controller = _makeController(api);
      addTearDown(controller.dispose);
      addTearDown(api.preview.close);
      controller.serverIpAllowlist = api.authoritative;
      const conflict = ControlProblem(
        status: 409,
        reasonCode: 'revision_conflict',
        messageKey: 'error.configuration_conflict',
      );
      api.saveError = conflict;
      final save = controller.saveServerIpAllowlist(['203.0.113.0/24']);
      await _waitForReads(api, 1);
      const current = ServerIpAllowlist(
        revision: 2,
        ranges: ['198.51.100.0/24'],
        maxRanges: 256,
      );
      api.reads.single.complete(current);
      expect(await save, same(conflict));
      expect(controller.serverIpAllowlist, same(current));
      expect(controller.serverIpAllowlistSaving, isFalse);
    },
  );

  test('disposed allowlist read cannot publish', () async {
    final api = _DelayedAllowlistApi();
    final controller = _makeController(api);
    addTearDown(api.preview.close);
    controller.serverIpAllowlist = api.authoritative;
    final read = controller.refreshServerManagement(quiet: true);
    await _waitForReads(api, 1);
    controller.dispose();
    api.reads.single.complete(
      const ServerIpAllowlist(revision: 9, ranges: [], maxRanges: 256),
    );
    await read;
    expect(controller.serverIpAllowlist?.revision, 1);
  });
}

WorkbenchController _makeController(_DelayedAllowlistApi api) =>
    WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      serverManagement: true,
      terminalManagement: false,
      closeRuntime: api.preview.close,
      initialPreferences: const WorkbenchPreferences(
        section: WorkbenchSection.settings,
      ),
    );

Future<void> _waitForReads(_DelayedAllowlistApi api, int count) async {
  for (var pass = 0; pass < 20 && api.reads.length < count; pass++) {
    await Future<void>.delayed(Duration.zero);
  }
  expect(api.reads, hasLength(count));
}
