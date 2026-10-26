import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/transparent_capture.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/settings_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

const _channel = MethodChannel('io.vibermate.desktop/transparent-capture');

Future<WorkbenchController> _mountSettings(
  WidgetTester tester, {
  AppLanguage language = AppLanguage.english,
}) async {
  final api = PreviewControlApi();
  final controller = WorkbenchController(
    api: api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: false,
    terminalManagement: false,
    runtimeTarget: 'https://remote.example.test',
    closeRuntime: api.close,
    initialPreferences: WorkbenchPreferences(
      section: WorkbenchSection.settings,
      language: language,
    ),
  );
  addTearDown(controller.dispose);
  await controller.initialize();
  await tester.pumpWidget(
    MaterialApp(
      theme: ViberTheme.light(),
      home: Scaffold(
        body: AnimatedBuilder(
          animation: controller,
          builder: (context, _) => SettingsView(
            controller: controller,
            copy: AppCopy.forLanguage(controller.language),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return controller;
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  WidgetController.hitTestWarningShouldBeFatal = true;

  tearDown(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(_channel, null);
  });

  for (final language in AppLanguage.values) {
    testWidgets(
      '390px Mac capture approval, activation and disable ($language)',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(390, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        var openedSettings = false;
        final enabledRequests = <bool>[];
        Map<String, Object?> nativeStatus = {
          'state': 'not_installed',
          'enabled': false,
        };
        tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
          _channel,
          (call) async {
            if (call.method == 'setEnabled') {
              final enabled = (call.arguments as Map)['enabled'] as bool;
              enabledRequests.add(enabled);
              nativeStatus = {
                'state': enabled ? 'approval_required' : 'disabled',
                'enabled': enabled,
              };
            } else if (call.method == 'openSystemSettings') {
              openedSettings = true;
              return null;
            }
            return nativeStatus;
          },
        );
        final controller = await _mountSettings(tester, language: language);
        final copy = AppCopy.forLanguage(language);
        final toggle = find.byKey(const Key('transparent-capture-toggle'));
        expect(toggle, findsOneWidget);
        expect(
          find.text(copy('settings.transparent_capture.state.not_installed')),
          findsOneWidget,
        );
        await tester.ensureVisible(toggle);
        await tester.tap(toggle);
        await tester.pumpAndSettle();
        expect(enabledRequests, [true]);
        expect(
          find.text(
            copy('settings.transparent_capture.state.approval_required'),
          ),
          findsOneWidget,
        );
        final openSettings = find.byKey(
          const Key('transparent-capture-system-settings'),
        );
        await tester.ensureVisible(openSettings);
        await tester.tap(openSettings);
        await tester.pumpAndSettle();
        expect(openedSettings, isTrue);

        nativeStatus = {'state': 'active', 'enabled': true};
        await tester.binding.defaultBinaryMessenger.handlePlatformMessage(
          _channel.name,
          _channel.codec.encodeMethodCall(
            MethodCall('statusChanged', nativeStatus),
          ),
          (_) {},
        );
        await tester.pumpAndSettle();
        expect(
          find.text(copy('settings.transparent_capture.state.active')),
          findsOneWidget,
        );
        await tester.ensureVisible(toggle);
        await tester.tap(toggle);
        await tester.pumpAndSettle();
        expect(enabledRequests, [true, false]);
        expect(
          find.text(copy('settings.transparent_capture.state.disabled')),
          findsOneWidget,
        );
        expect(tester.takeException(), isNull);
        controller.dispose();
        await tester.pumpWidget(const SizedBox.shrink());
      },
      variant: TargetPlatformVariant.only(TargetPlatform.macOS),
    );
  }

  testWidgets(
    'Host capture failure remains visible through native status refresh',
    (tester) async {
      const reason =
          'flow registration socket path exceeds the Unix socket limit';
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        _channel,
        (_) async => {'state': 'not_installed', 'enabled': false},
      );
      final capture = TransparentCaptureController.shared;
      await capture.configure(instanceId: 'host-failed', hostError: reason);
      final controller = await _mountSettings(tester);
      expect(find.text(reason), findsOneWidget);
      expect(find.text('Unavailable'), findsOneWidget);
      expect(
        tester
            .widget<Switch>(find.byKey(const Key('transparent-capture-toggle')))
            .onChanged,
        isNull,
      );
      await capture.refresh();
      await tester.pumpAndSettle();
      expect(find.text(reason), findsOneWidget);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await capture.clearConfiguration('host-failed');
    },
    variant: TargetPlatformVariant.only(TargetPlatform.macOS),
  );

  testWidgets(
    'late enable response cannot roll back native active notification',
    (tester) async {
      final response = Completer<Map<String, Object?>>();
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        _channel,
        (call) async => call.method == 'setEnabled'
            ? response.future
            : {'state': 'disabled', 'enabled': false},
      );
      final controller = await _mountSettings(tester);
      final toggle = find.byKey(const Key('transparent-capture-toggle'));
      await tester.ensureVisible(toggle);
      await tester.tap(toggle);
      await tester.pump();
      expect(tester.widget<Switch>(toggle).onChanged, isNull);
      await tester.binding.defaultBinaryMessenger.handlePlatformMessage(
        _channel.name,
        _channel.codec.encodeMethodCall(
          const MethodCall('statusChanged', {
            'state': 'active',
            'enabled': true,
          }),
        ),
        (_) {},
      );
      response.complete({'state': 'approval_required', 'enabled': true});
      await tester.pumpAndSettle();
      expect(find.text('Active'), findsOneWidget);
      expect(tester.widget<Switch>(toggle).value, isTrue);
      expect(tester.widget<Switch>(toggle).onChanged, isNotNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    },
    variant: TargetPlatformVariant.only(TargetPlatform.macOS),
  );

  testWidgets(
    'native failure remains actionable when capture is enabled',
    (tester) async {
      const reason = 'Network configuration was denied';
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        _channel,
        (call) async => call.method == 'setEnabled'
            ? {'state': 'disabled', 'enabled': false}
            : {'state': 'unavailable', 'enabled': true, 'error': reason},
      );
      final controller = await _mountSettings(tester);
      final toggle = find.byKey(const Key('transparent-capture-toggle'));
      expect(find.text(reason), findsOneWidget);
      expect(tester.widget<Switch>(toggle).value, isTrue);
      await tester.ensureVisible(toggle);
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(tester.widget<Switch>(toggle).value, isFalse);
      expect(find.text(reason), findsNothing);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    },
    variant: TargetPlatformVariant.only(TargetPlatform.macOS),
  );

  testWidgets(
    'native failure can be retried with current Runtime configuration',
    (tester) async {
      const reason = 'Network configuration was denied';
      final requests = <bool>[];
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(_channel, (
        call,
      ) async {
        if (call.method == 'setEnabled') {
          requests.add((call.arguments as Map)['enabled'] as bool);
          return {'state': 'starting', 'enabled': true};
        }
        return {'state': 'unavailable', 'enabled': false, 'error': reason};
      });
      final capture = TransparentCaptureController.shared;
      await capture.configure(
        instanceId: 'retry-generation',
        flowPort: 32123,
        flowSecret: 'F' * 43,
      );
      final controller = await _mountSettings(tester);
      final toggle = find.byKey(const Key('transparent-capture-toggle'));
      expect(find.text(reason), findsOneWidget);
      expect(tester.widget<Switch>(toggle).onChanged, isNotNull);
      await tester.ensureVisible(toggle);
      await tester.tap(toggle);
      await tester.pumpAndSettle();
      expect(requests, [true]);
      expect(find.text('Starting…'), findsOneWidget);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
      await capture.clearConfiguration('retry-generation');
    },
    variant: TargetPlatformVariant.only(TargetPlatform.macOS),
  );

  testWidgets(
    'missing native plugin shows unavailable without breaking Settings',
    (tester) async {
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        _channel,
        (_) async => throw MissingPluginException(),
      );
      final controller = await _mountSettings(tester);
      expect(
        find.byKey(const Key('transparent-capture-panel')),
        findsOneWidget,
      );
      expect(find.text('Unavailable'), findsOneWidget);
      final toggle = tester.widget<Switch>(
        find.byKey(const Key('transparent-capture-toggle')),
      );
      expect(toggle.onChanged, isNull);
      expect(tester.takeException(), isNull);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    },
    variant: TargetPlatformVariant.only(TargetPlatform.macOS),
  );

  testWidgets(
    'non-Mac Settings never calls the native capture channel',
    (tester) async {
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(_channel, (
        call,
      ) async {
        fail('non-Mac settings called ${call.method}');
      });
      final controller = await _mountSettings(tester);
      expect(find.byKey(const Key('transparent-capture-panel')), findsNothing);
      controller.dispose();
      await tester.pumpWidget(const SizedBox.shrink());
    },
    variant: TargetPlatformVariant.only(TargetPlatform.linux),
  );
}
