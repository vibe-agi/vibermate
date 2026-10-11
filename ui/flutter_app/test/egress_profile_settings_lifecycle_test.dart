import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

const _profileID = 'profile.shared';
const _policy = TrafficEgressPolicy(
  proxy: TrafficProxyPolicy(kind: 'socks5', endpoint: '127.0.0.1:1080'),
  resolver: TrafficResolverPolicy.system(),
);

void main() {
  for (final edit in [false, true]) {
    testWidgets(
      'late ${edit ? 'edit' : 'create'} dialog cannot write new Runtime',
      (tester) async {
        final h = await _mount(tester);
        final catalog = await h.b.delegate.egressProfiles();
        final setting = await h.b.delegate.egressDefault();
        await h.open(tester, edit: edit);
        await h.replace(tester, h.controllerB);
        expect(
          find.byKey(const Key('settings-egress-profile-dialog')),
          findsOneWidget,
        );
        await h.publish(tester);
        expect(h.a.writes, isEmpty);
        expect(h.b.writes, isEmpty);
        expect((await h.b.delegate.egressProfiles()).items, catalog.items);
        expect((await h.b.delegate.egressDefault()).revision, setting.revision);
        expect((await h.b.delegate.egressDefault()).profile, setting.profile);
        expect(find.byKey(const Key('egress-default-summary')), findsOneWidget);
        expect(tester.takeException(), isNull);
      },
    );
    testWidgets(
      'ABA ${edit ? 'edit' : 'create'} dialog cannot resurrect intent',
      (tester) async {
        final h = await _mount(tester);
        await h.open(tester, edit: edit);
        await h.replace(tester, h.controllerB);
        await h.replace(tester, h.controllerA);
        await h.publish(tester);
        expect(h.a.writes, isEmpty);
        expect(h.b.writes, isEmpty);
        expect(tester.takeException(), isNull);
      },
    );
    testWidgets(
      'same scope ${edit ? 'edit' : 'create'} survives ordinary rebuild',
      (tester) async {
        final h = await _mount(tester);
        await h.open(tester, edit: edit);
        await h.replace(tester, h.controllerA);
        await h.publish(tester);
        expect(h.a.writes, hasLength(1));
        expect(h.b.writes, isEmpty);
        final write = h.a.writes.single;
        expect(write.revision, edit ? 1 : 0);
        expect(write.name, 'Chosen profile');
        expect(
          write.policy,
          edit ? _policy : EgressProfileRevision.direct.policy,
        );
        if (edit) expect(write.id, _profileID);
        final saved = (await h.a.delegate.egressProfiles()).items.singleWhere(
          (p) => p.id == write.id,
        );
        expect(saved.revision, edit ? 2 : 1);
        expect(saved.displayName, 'Chosen profile');
        expect(tester.takeException(), isNull);
      },
    );
    for (final unmount in [false, true]) {
      testWidgets(
        '${edit ? 'edit' : 'create'} ${unmount ? 'unmount' : 'cancel'} sends nothing',
        (tester) async {
          final h = await _mount(tester);
          await h.open(tester, edit: edit);
          if (unmount) {
            await tester.pumpWidget(const SizedBox.shrink());
          } else {
            await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
          }
          await tester.pumpAndSettle();
          expect(h.a.writes, isEmpty);
          expect(h.b.writes, isEmpty);
          expect(tester.takeException(), isNull);
        },
      );
    }
    for (final failure in [false, true]) {
      testWidgets(
        'late ${failure ? 'failure' : 'success'} ack for ${edit ? 'edit' : 'create'} cannot alter B',
        (tester) async {
          final h = await _mount(tester);
          h.a.pending = Completer<void>();
          h.a.failWrite = failure;
          await h.open(tester, edit: edit);
          await h.publish(tester);
          expect(h.a.writes, hasLength(1));
          await h.replace(tester, h.controllerB);
          final catalogReads = h.b.catalogReads,
              defaultReads = h.b.defaultReads;
          final catalog = await h.b.delegate.egressProfiles();
          final setting = await h.b.delegate.egressDefault();
          h.a.pending!.complete();
          await tester.pumpAndSettle();
          expect(h.a.writes, hasLength(1));
          expect(h.b.writes, isEmpty);
          expect(h.b.catalogReads, catalogReads);
          expect(h.b.defaultReads, defaultReads);
          expect((await h.b.delegate.egressProfiles()).items, catalog.items);
          expect(
            (await h.b.delegate.egressDefault()).revision,
            setting.revision,
          );
          expect(
            find.byKey(const Key('egress-default-summary')),
            findsOneWidget,
          );
          expect(find.text('Runtime B profile'), findsOneWidget);
          expect(
            tester
                .widget<FilledButton>(
                  find.byKey(const Key('egress-profile-add')),
                )
                .onPressed,
            isNotNull,
          );
          expect(tester.takeException(), isNull);
        },
      );
    }
  }
  for (final failure in [false, true]) {
    testWidgets(
      'old ${failure ? 'failure' : 'success'} ack cannot clear B active saving',
      (tester) async {
        final h = await _mount(tester);
        h.a.pending = Completer<void>();
        h.a.failWrite = failure;
        await h.open(tester, edit: true);
        await h.publish(tester);
        await h.replace(tester, h.controllerB);
        h.b.pending = Completer<void>();
        await h.open(tester, edit: false);
        await h.publish(tester);
        expect(h.a.writes, hasLength(1));
        expect(h.b.writes, hasLength(1));
        final reads = h.b.catalogReads;
        h.a.pending!.complete();
        await tester.pumpAndSettle();
        expect(h.b.catalogReads, reads);
        expect(
          tester
              .widget<FilledButton>(find.byKey(const Key('egress-profile-add')))
              .onPressed,
          isNull,
        );
        expect(find.byKey(const Key('egress-default-summary')), findsOneWidget);
        h.b.pending!.complete();
        await tester.pumpAndSettle();
        expect(h.b.writes, hasLength(1));
        expect(
          tester
              .widget<FilledButton>(find.byKey(const Key('egress-profile-add')))
              .onPressed,
          isNotNull,
        );
        expect(tester.takeException(), isNull);
      },
    );
  }
}

final class _ProfileApi implements ControlApi {
  final delegate = PreviewControlApi(seedCaptures: false);
  final writes =
      <({String id, int revision, String name, TrafficEgressPolicy policy})>[];
  int catalogReads = 0;
  int defaultReads = 0;
  Completer<void>? pending;
  bool failWrite = false;

  @override
  Future<EgressProfileCatalog> egressProfiles() {
    catalogReads++;
    return delegate.egressProfiles();
  }

  @override
  Future<EgressDefaultSetting> egressDefault() {
    defaultReads++;
    return delegate.egressDefault();
  }

  @override
  Future<EgressProfileRevision> publishEgressProfile({
    required String id,
    required int expectedRevision,
    required String displayName,
    required TrafficEgressPolicy policy,
  }) async {
    writes.add((
      id: id,
      revision: expectedRevision,
      name: displayName,
      policy: policy,
    ));
    if (pending != null) await pending!.future;
    if (failWrite) throw StateError('synthetic write failure');
    return delegate.publishEgressProfile(
      id: id,
      expectedRevision: expectedRevision,
      displayName: displayName,
      policy: policy,
    );
  }

  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnsupportedError('unexpected API call');
}

final class _Harness {
  _Harness(this.a, this.b, this.controllerA, this.controllerB)
    : current = controllerA;
  final _ProfileApi a, b;
  final WorkbenchController controllerA, controllerB;
  WorkbenchController current;
  late StateSetter rebuild;

  Future<void> replace(
    WidgetTester tester,
    WorkbenchController controller,
  ) async {
    rebuild(() => current = controller);
    await tester.pumpAndSettle();
  }

  Future<void> open(WidgetTester tester, {required bool edit}) async {
    final action = find.byKey(
      Key(edit ? 'egress-profile-edit-$_profileID' : 'egress-profile-add'),
    );
    await tester.ensureVisible(action);
    await tester.tap(action);
    await tester.pumpAndSettle();
    expect(
      find.byKey(const Key('settings-egress-profile-dialog')),
      findsOneWidget,
    );
    await tester.enterText(
      find.byKey(const Key('egress-profile-display-name')),
      'Chosen profile',
    );
  }

  Future<void> publish(WidgetTester tester) async {
    await tester.tap(find.byKey(const Key('egress-profile-publish')));
    await tester.pumpAndSettle();
  }
}

Future<_Harness> _mount(WidgetTester tester) async {
  await tester.binding.setSurfaceSize(const Size(1100, 1000));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final a = _ProfileApi(), b = _ProfileApi();
  for (final api in [a, b]) {
    await api.delegate.publishEgressProfile(
      id: _profileID,
      expectedRevision: 0,
      displayName: identical(api, a)
          ? 'Runtime A profile'
          : 'Runtime B profile',
      policy: _policy,
    );
    addTearDown(() async {
      if (api.pending case final pending? when !pending.isCompleted) {
        pending.complete();
      }
      await api.delegate.close();
    });
  }
  Future<WorkbenchController> controller(_ProfileApi api) async {
    final value = WorkbenchController(
      api: api,
      terminalCommands: PreviewTerminalCommandService(),
      previewMode: true,
      closeRuntime: () async {},
    );
    value.data = await api.delegate.loadDashboard();
    value.loading = false;
    value.selectSettingsTab(value.settingsDestinations.length - 1);
    addTearDown(value.dispose);
    return value;
  }

  final h = _Harness(a, b, await controller(a), await controller(b));
  await tester.pumpWidget(
    MaterialApp(
      theme: ViberTheme.light(),
      home: Scaffold(
        body: StatefulBuilder(
          builder: (_, setState) {
            h.rebuild = setState;
            return SettingsView(
              controller: h.current,
              copy: AppCopy.forLanguage(AppLanguage.english),
            );
          },
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  expect(
    find.byKey(const Key('egress-profile-row-$_profileID')),
    findsOneWidget,
  );
  return h;
}
