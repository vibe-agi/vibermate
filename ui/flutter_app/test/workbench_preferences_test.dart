import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/workbench_window_appearance.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';

void main() {
  const complete = WorkbenchPreferences(
    language: AppLanguage.simplifiedChinese,
    theme: WorkbenchTheme.dark,
    section: WorkbenchSection.captures,
    selectedCaptureKey: 'managed_run:run-7',
    selectedEnvironmentId: 'environment.work',
    selectedEnvironmentRevision: 7,
    selectedEndpointId: 'target.custom.anthropic.primary',
  );

  test('workbench preferences round-trip one closed nonsecret contract', () {
    expect(WorkbenchPreferences.decode(complete.encode()), complete);
    final payload = jsonDecode(complete.encode()) as Map<String, Object?>;
    expect(payload.keys.toSet(), {
      'schema',
      'language',
      'theme',
      'section',
      'selectedCaptureKey',
      'selectedEnvironmentId',
      'selectedEnvironmentRevision',
      'selectedEndpointId',
    });
    expect(complete.encode(), isNot(contains('credential')));
  });

  test('unknown fields and invalid selectors are rejected', () {
    final payload = jsonDecode(complete.encode()) as Map<String, Object?>;
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({...payload, 'secret': 'must-not-be-stored'}),
      ),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({...payload, 'selectedCaptureKey': '../../escape'}),
      ),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({...payload, 'language': 'automatic'}),
      ),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({...payload, 'theme': 'sepia'}),
      ),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({
          ...payload,
          'selectedEnvironmentId': null,
          'selectedEnvironmentRevision': 7,
        }),
      ),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({...payload, 'selectedEnvironmentRevision': 0}),
      ),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
  });

  test('system theme is the default and remains explicit on the wire', () {
    const preferences = WorkbenchPreferences();
    expect(preferences.theme, WorkbenchTheme.system);
    expect(
      WorkbenchPreferences.decode(preferences.encode()).theme,
      WorkbenchTheme.system,
    );
    expect(
      (jsonDecode(preferences.encode()) as Map<String, Object?>)['theme'],
      'system',
    );
  });

  test('future schema is distinguished so an older app preserves it', () {
    final payload = jsonDecode(complete.encode()) as Map<String, Object?>;
    expect(
      () => WorkbenchPreferences.decode(
        jsonEncode({
          ...payload,
          'schema': 'vibermate-workbench-preferences/v3',
        }),
      ),
      throwsA(isA<WorkbenchPreferencesFutureSchema>()),
    );
  });

  test('loader repairs corrupt state but fences a future schema', () async {
    final corruptStore = MemoryWorkbenchPreferencesStore(encoded: '{bad');
    final corrupt = await loadWorkbenchPreferences(
      corruptStore,
      fallbackLanguage: AppLanguage.english,
    );
    expect(corrupt.value, const WorkbenchPreferences());
    expect(corrupt.writable, isTrue);
    expect(corrupt.issue, WorkbenchPreferencesIssue.corrupt);

    final payload = jsonDecode(complete.encode()) as Map<String, Object?>;
    final futureStore = MemoryWorkbenchPreferencesStore(
      encoded: jsonEncode({
        ...payload,
        'schema': 'vibermate-workbench-preferences/v3',
      }),
    );
    final future = await loadWorkbenchPreferences(
      futureStore,
      fallbackLanguage: AppLanguage.english,
    );
    expect(future.value, const WorkbenchPreferences());
    expect(future.writable, isFalse);
    expect(future.issue, WorkbenchPreferencesIssue.futureSchema);
    expect(futureStore.encoded, contains('vibermate-workbench-preferences/v3'));
  });

  test('platform store accepts only the exact bounded contract', () async {
    TestWidgetsFlutterBinding.ensureInitialized();
    const channel = MethodChannel('io.vibermate.desktop/preferences');
    String? written;
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, (call) async {
          switch (call.method) {
            case 'readWorkbenchPreferences':
              expect(call.arguments, isNull);
              return complete.encode();
            case 'writeWorkbenchPreferences':
              written = call.arguments as String;
              return null;
            default:
              fail('Unexpected preference method ${call.method}');
          }
        });
    addTearDown(
      () => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null),
    );

    const store = PlatformWorkbenchPreferencesStore();
    expect(await store.read(), complete.encode());
    await store.write(complete.encode());
    expect(WorkbenchPreferences.decode(written!), complete);
    expect(
      () => store.write('{"arbitrary":true}'),
      throwsA(isA<WorkbenchPreferencesException>()),
    );
  });

  test('window appearance channel sends the closed theme value', () async {
    TestWidgetsFlutterBinding.ensureInitialized();
    const channel = MethodChannel('io.vibermate.desktop/preferences');
    final observed = <Object?>[];
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, (call) async {
          expect(call.method, 'setWorkbenchTheme');
          observed.add(call.arguments);
          return null;
        });
    addTearDown(
      () => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null),
    );

    const appearance = PlatformWorkbenchWindowAppearance();
    await appearance.apply(WorkbenchTheme.system);
    await appearance.apply(WorkbenchTheme.light);
    await appearance.apply(WorkbenchTheme.dark);
    expect(observed, ['system', 'light', 'dark']);
  });
}
