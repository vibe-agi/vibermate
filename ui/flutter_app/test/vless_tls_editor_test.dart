import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:file_selector_platform_interface/file_selector_platform_interface.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/egress_profile_editor.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';

const tlsNode =
    'vless://00000000-0000-4000-8000-000000000001@node.invalid:443?security=tls';
const realityNode =
    'vless://00000000-0000-4000-8000-000000000001@node.invalid:443?security=reality';

void main() {
  late _Files files;
  setUp(() {
    final original = FileSelectorPlatform.instance;
    files = _Files();
    FileSelectorPlatform.instance = files;
    addTearDown(() => FileSelectorPlatform.instance = original);
  });
  test('preview does not fabricate private settings', () async {
    final preview = PreviewControlApi();
    addTearDown(preview.close);
    await expectLater(
      preview.vlessProfileDescription('profile.fixture', 1),
      throwsA(isA<UnsupportedError>()),
    );
  });
  for (final custom in [false, true]) {
    testWidgets(
      'new TLS ${custom ? 'custom CA' : 'System'} saves private intent',
      (tester) async {
        final h = await _open(tester);
        if (custom) {
          await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
          expect(_saveButton(tester).onPressed, isNull);
          await _paste(tester, 'fixture PEM');
        }
        await _save(tester);
        final draft = h.result as VlessEgressProfileDraft;
        expect(draft.import.uri, tlsNode);
        expect(draft.import.tls?.mode, custom ? 'custom_ca' : null);
        expect(draft.import.tls?.caPem, custom ? 'fixture PEM' : null);
        expect(draft.import.toString(), isNot(contains('fixture PEM')));
      },
    );
  }
  testWidgets('REALITY never offers TLS input', (tester) async {
    final h = await _open(tester, link: realityNode);
    expect(find.byKey(const Key('vless-tls-mode')), findsNothing);
    await _save(tester);
    expect((h.result as VlessEgressProfileDraft).import.tls, isNull);
  });
  testWidgets('malformed and ambiguous link detection stays safe', (
    tester,
  ) async {
    await _open(tester);
    for (final link in [
      '$tlsNode&other=%FF',
      '$tlsNode&security=tls',
      'vless://node.invalid?security=%FF',
      'not a node link',
    ]) {
      await tester.enterText(
        find.byKey(const Key('egress-profile-vless-uri')),
        link,
      );
      await tester.pump();
      expect(tester.takeException(), isNull);
    }
  });
  testWidgets('replacement link resets selected custom CA to System', (
    tester,
  ) async {
    final h = await _open(tester);
    await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
    await _paste(tester, 'old-node CA');
    await tester.enterText(
      find.byKey(const Key('egress-profile-vless-uri')),
      '$tlsNode&sni=new.invalid',
    );
    await tester.pumpAndSettle();
    expect(find.text('Use system certificates'), findsOneWidget);
    expect(find.text('CA certificate selected'), findsNothing);
    await _save(tester);
    expect((h.result as VlessEgressProfileDraft).import.tls, isNull);
  });
  testWidgets('clearing new paste cannot manufacture a saved CA', (
    tester,
  ) async {
    final h = await _open(tester);
    await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
    await _paste(tester, 'fixture PEM');
    await tester.enterText(find.byKey(const Key('vless-tls-pem')), '');
    await tester.pump();
    expect(_saveButton(tester).onPressed, isNull);
    expect(h.result, isNull);
    expect(
      find.text('A custom CA is saved. It will be kept unless replaced.'),
      findsNothing,
    );
  });
  testWidgets('paste is bounded by UTF-8 bytes', (tester) async {
    await _open(tester);
    await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
    await _paste(tester, '中' * 22000);
    expect(_saveButton(tester).onPressed, isNull);
    expect(
      find.text('Choose a PEM CA certificate file no larger than 64 KiB.'),
      findsOneWidget,
    );
  });
  for (final invalid in ['empty', 'size', 'utf8', 'chunk']) {
    testWidgets('CA file rejects $invalid without exposing private contents', (
      tester,
    ) async {
      final h = await _open(tester);
      await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
      final data = switch (invalid) {
        'empty' => Uint8List(0),
        'size' => Uint8List(65537),
        'utf8' => Uint8List.fromList([0xff, 0xfe]),
        _ => Uint8List(0),
      };
      files.next = () async => invalid == 'chunk'
          ? _ChunkFile()
          : XFile.fromData(data, name: 'private-fixture.pem');
      await _file(tester);
      await tester.pumpAndSettle();
      expect(
        find.text('Choose a PEM CA certificate file no larger than 64 KiB.'),
        findsOneWidget,
      );
      expect(find.text('private-fixture.pem'), findsNothing);
      expect(_saveButton(tester).onPressed, isNull);
      expect(h.result, isNull);
      expect(tester.takeException(), isNull);
    });
  }
  testWidgets(
    'file import supports exactly 64 KiB and cancel keeps prior selection',
    (tester) async {
      final h = await _open(tester);
      await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
      final pem = 'a' * 65536;
      files.next = () async => XFile.fromData(
        Uint8List.fromList(utf8.encode(pem)),
        name: 'private.pem',
      );
      await _file(tester);
      await tester.pumpAndSettle();
      expect(find.text('CA certificate selected'), findsOneWidget);
      files.next = () async => null;
      await _file(tester);
      await tester.pumpAndSettle();
      await _save(tester);
      expect((h.result as VlessEgressProfileDraft).import.tls?.caPem, pem);
    },
  );
  for (final action in ['paste', 'mode', 'kind', 'link', 'dispose']) {
    testWidgets('delayed file $action cannot overwrite a later context', (
      tester,
    ) async {
      final h = await _open(tester);
      await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
      final pending = Completer<XFile?>();
      files.next = () => pending.future;
      await _file(tester, pending: true);
      expect(_saveButton(tester).onPressed, isNull);
      switch (action) {
        case 'paste':
          await _paste(tester, 'later CA');
        case 'mode':
          await _choose(tester, 'vless-tls-mode', 'vless-tls-system');
        case 'kind':
          await _choose(
            tester,
            'egress-profile-proxy-kind',
            'egress-profile-proxy-direct',
          );
        case 'link':
          await tester.enterText(
            find.byKey(const Key('egress-profile-vless-uri')),
            '$tlsNode&sni=later.invalid',
          );
          await tester.pump();
        case 'dispose':
          await tester.pumpWidget(const SizedBox.shrink());
      }
      pending.complete(
        XFile.fromData(Uint8List.fromList(utf8.encode('stale CA'))),
      );
      await tester.pumpAndSettle();
      expect(find.text('stale CA'), findsNothing);
      if (action != 'dispose') {
        await _save(tester);
        if (action == 'kind') {
          expect(h.result, isA<EgressProfileDraft>());
        } else {
          expect(
            (h.result as VlessEgressProfileDraft).import.tls?.caPem,
            action == 'paste' ? 'later CA' : null,
          );
        }
      }
      expect(tester.takeException(), isNull);
    });
  }
  testWidgets('delayed stream after link change is discarded', (tester) async {
    final h = await _open(tester);
    await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
    final stream = StreamController<Uint8List>();
    addTearDown(stream.close);
    files.next = () async => _DelayedStreamFile(stream.stream);
    await _file(tester, pending: true);
    await tester.pump();
    await tester.enterText(
      find.byKey(const Key('egress-profile-vless-uri')),
      '$tlsNode&sni=later.invalid',
    );
    await tester.pump();
    await tester.runAsync(() async {
      stream.add(Uint8List.fromList(utf8.encode('stale CA')));
      await stream.close();
    });
    await tester.pumpAndSettle();
    await _save(tester);
    expect((h.result as VlessEgressProfileDraft).import.tls, isNull);
    expect(tester.takeException(), isNull);
  });
  for (final lang in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final width in [390.0, 1100.0]) {
      testWidgets('TLS custom CA controls reachable at ${width}px $lang', (
        tester,
      ) async {
        final h = await _open(tester, language: lang, width: width);
        await _choose(tester, 'vless-tls-mode', 'vless-tls-custom_ca');
        await _paste(tester, 'fixture PEM');
        await tester.ensureVisible(find.byKey(const Key('vless-tls-file')));
        expect(
          tester.getRect(find.byKey(const Key('vless-tls-file'))).right,
          lessThanOrEqualTo(width),
        );
        expect(
          tester.getRect(find.byKey(const Key('egress-profile-publish'))).right,
          lessThanOrEqualTo(width),
        );
        await _save(tester);
        expect(h.result, isA<VlessEgressProfileDraft>());
        expect(tester.takeException(), isNull);
      });
    }
  }
}

final class _Files extends FileSelectorPlatform {
  Future<XFile?> Function() next = () async => null;
  Completer<void>? completed;
  @override
  Future<XFile?> openFile({
    List<XTypeGroup>? acceptedTypeGroups,
    String? initialDirectory,
    String? confirmButtonText,
  }) async {
    final done = completed = Completer<void>();
    final selected = await next();
    if (selected == null) {
      done.complete();
      return null;
    }
    return _ObservedFile(selected, done);
  }
}

// Await browser FileReader events, not fake frame time, for actual XFile reads.
final class _ObservedFile extends XFile {
  _ObservedFile(this.source, this.completed) : super.fromData(Uint8List(0));
  final XFile source;
  final Completer<void> completed;
  void _done() {
    if (!completed.isCompleted) completed.complete();
  }

  @override
  Future<int> length() async {
    final size = await source.length();
    if (size <= 0 || size > 65536) _done();
    return size;
  }

  @override
  Stream<Uint8List> openRead([int? start, int? end]) async* {
    try {
      yield* source.openRead(start, end);
    } finally {
      _done();
    }
  }
}

final class _ChunkFile extends XFile {
  _ChunkFile() : super.fromData(Uint8List(0));
  @override
  Future<int> length() async => 1;
  @override
  Stream<Uint8List> openRead([int? start, int? end]) =>
      Stream.fromIterable([Uint8List(65536), Uint8List(1)]);
}

final class _DelayedStreamFile extends XFile {
  _DelayedStreamFile(this.stream) : super.fromData(Uint8List(0));
  final Stream<Uint8List> stream;
  @override
  Future<int> length() async => 8;
  @override
  Stream<Uint8List> openRead([int? start, int? end]) => stream;
}

final class _Dialog {
  EgressProfileEditIntent? result;
}

FilledButton _saveButton(WidgetTester tester) =>
    tester.widget(find.byKey(const Key('egress-profile-publish')));
Future<void> _save(WidgetTester tester) async {
  await tester.tap(find.byKey(const Key('egress-profile-publish')));
  await tester.pumpAndSettle();
}

Future<void> _file(WidgetTester tester, {bool pending = false}) async {
  await tester.ensureVisible(find.byKey(const Key('vless-tls-file')));
  await tester.runAsync(() async {
    await tester.tap(find.byKey(const Key('vless-tls-file')));
    if (!pending) {
      await (FileSelectorPlatform.instance as _Files).completed!.future;
    }
    await Future<void>.delayed(Duration.zero);
  });
  await tester.pump();
}

Future<void> _paste(WidgetTester tester, String pem) async {
  await tester.ensureVisible(find.byKey(const Key('vless-tls-paste-toggle')));
  await tester.tap(find.byKey(const Key('vless-tls-paste-toggle')));
  await tester.pump();
  await tester.enterText(find.byKey(const Key('vless-tls-pem')), pem);
  await tester.pump();
}

Future<void> _choose(WidgetTester tester, String picker, String option) async {
  await tester.ensureVisible(find.byKey(Key(picker)));
  await tester.tap(find.byKey(Key(picker)));
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 350));
  await tester.tap(find.byKey(Key(option)).last);
  await tester.pump(const Duration(milliseconds: 350));
  await tester.pump();
}

Future<_Dialog> _open(
  WidgetTester tester, {
  String link = tlsNode,
  AppLanguage language = AppLanguage.english,
  double width = 1100,
}) async {
  await tester.binding.setSurfaceSize(Size(width, 844));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final h = _Dialog();
  await tester.pumpWidget(
    MaterialApp(
      theme: ViberTheme.light(),
      home: Scaffold(
        body: Builder(
          builder: (context) => TextButton(
            onPressed: () async {
              h.result = await showEgressProfileEditor(
                context: context,
                copy: AppCopy.forLanguage(language),
              );
            },
            child: const Text('Open'),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('Open'));
  await tester.pumpAndSettle();
  await tester.enterText(
    find.byKey(const Key('egress-profile-display-name')),
    'Chosen exit',
  );
  await _choose(
    tester,
    'egress-profile-proxy-kind',
    'egress-profile-proxy-vless',
  );
  await tester.enterText(
    find.byKey(const Key('egress-profile-vless-uri')),
    link,
  );
  await tester.pumpAndSettle();
  return h;
}
