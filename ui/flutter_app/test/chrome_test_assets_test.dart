@TestOn('vm')
library;

import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';

const _names = ['ink_sparkle.frag', 'stretch_effect.frag'];

void main() {
  test(
    'actual pinned compiler prepares both web shaders and repeat leaves bytes untouched',
    () async {
      final project = _project();
      await _runTool(project);
      final before = <List<int>>[];
      final stamp = DateTime.utc(2001);
      for (final name in _names) {
        final file = _output(project, name);
        final bytes = file.readAsBytesSync();
        final data = jsonDecode(utf8.decode(bytes)) as Map;
        expect((data['sksl'] as Map)['shader'], isA<String>());
        expect((data['sksl'] as Map)['uniforms'], isA<List>());
        before.add(bytes);
        file.setLastModifiedSync(stamp);
      }
      await _runTool(project);
      for (var i = 0; i < _names.length; i++) {
        expect(_output(project, _names[i]).readAsBytesSync(), before[i]);
        expect(_output(project, _names[i]).lastModifiedSync().toUtc(), stamp);
      }
    },
  );

  for (final failure in [
    'project',
    'missing package',
    'missing SDK',
    'wrong pin',
  ]) {
    test('refuses $failure before generating outputs', () async {
      final project = _project();
      switch (failure) {
        case 'project':
          File(
            '${project.path}/pubspec.yaml',
          ).writeAsStringSync('name: other\n');
        case 'missing package':
          File(
            '${project.path}/.dart_tool/package_config.json',
          ).writeAsStringSync('{"packages":[]}');
        case 'missing SDK':
          _selectSDK(project, Directory('${project.path}/absent'));
        case 'wrong pin':
          File('${project.path}/tool/flutter-sdk.env').writeAsStringSync(
            'VIBERMATE_FLUTTER_VERSION=0.0.0\nVIBERMATE_FLUTTER_REVISION=${'0' * 40}\n',
          );
      }
      await _runTool(
        project,
        failure: switch (failure) {
          'project' => 'Expected the vibermate_app Flutter project.',
          'missing package' => 'Expected one resolved package:flutter.',
          'missing SDK' => 'Cannot resolve symbolic links',
          _ => 'Resolved Flutter SDK does not match',
        },
      );
      _noOutputs(project);
    });
  }

  for (final failure in [
    'wrong SDK',
    'missing compiler',
    'compiler exit',
    'invalid JSON',
  ]) {
    test(
      '$failure preserves diagnostics and publishes neither shader',
      () async {
        final project = _project();
        await _privateSDK(project, failure);
        await _runTool(
          project,
          failure: switch (failure) {
            'wrong SDK' => 'Resolved Flutter SDK does not match',
            'missing compiler' => 'Missing cached pinned impellerc',
            'compiler exit' => 'impellerc failed for ink_sparkle.frag: exit=17',
            _ => 'Compiler output is not web shader JSON.',
          },
        );
        _noOutputs(project);
      },
    );
  }

  test(
    'a conflicting second target preserves it and never creates the first',
    () async {
      final project = _project();
      final file = _output(project, _names.last)
        ..parent.createSync(recursive: true);
      file.writeAsStringSync('user-owned bytes');
      await _runTool(project, failure: 'Refusing conflicting existing shader:');
      expect(file.readAsStringSync(), 'user-owned bytes');
      expect(_output(project, _names.first).existsSync(), isFalse);
    },
  );

  test('a directory target is not overwritten', () async {
    final project = _project();
    final directory = Directory(_output(project, _names.first).path)
      ..createSync(recursive: true);
    await _runTool(project, failure: 'Refusing non-regular shader target:');
    expect(directory.existsSync(), isTrue);
    expect(_output(project, _names.last).existsSync(), isFalse);
  });

  for (final target in ['parent', 'file', 'dangling']) {
    test(
      'pre-existing $target link is refused without touching its destination',
      () async {
        final project = _project();
        final outside = Directory.systemTemp.createTempSync(
          'chrome-assets-outside.',
        );
        addTearDown(() => outside.deleteSync(recursive: true));
        final sentinel = File('${outside.path}/sentinel')
          ..writeAsStringSync('unchanged');
        late Link link;
        if (target == 'parent') {
          link = Link('${project.path}/test/assets')..createSync(outside.path);
        } else {
          final path = _output(project, _names.first);
          path.parent.createSync(recursive: true);
          link = Link(path.path)
            ..createSync(
              target == 'file' ? sentinel.path : '${outside.path}/absent',
            );
        }
        final before = link.targetSync();
        await _runTool(
          project,
          failure: target == 'parent'
              ? 'Expected a real directory, not a link:'
              : 'Refusing non-regular shader target:',
        );
        expect(link.targetSync(), before);
        expect(sentinel.readAsStringSync(), 'unchanged');
        expect(outside.listSync().map((e) => e.path), [sentinel.path]);
      },
    );
  }
}

Future<void> _runTool(Directory project, {String? failure}) async {
  final dart = File('${_actualSDK().path}/bin/cache/dart-sdk/bin/dart');
  final script = File('tool/prepare_chrome_test_assets.dart').absolute;
  expect(dart.existsSync(), isTrue);
  expect(script.existsSync(), isTrue);
  // Runtime CLI invocation: no cross-root static import for the web compiler,
  // no flutter_tester executable and no Dartdev/pub in the synthetic project.
  final process = await Process.start(dart.path, [
    '--disable-dart-dev',
    script.path,
  ], workingDirectory: project.path);
  final output = process.stdout.transform(utf8.decoder).join();
  final errors = process.stderr.transform(utf8.decoder).join();
  await process.stdin.close();
  final code = await process.exitCode.timeout(
    const Duration(seconds: 75),
    onTimeout: () async {
      process.kill(ProcessSignal.sigkill);
      await process.exitCode;
      await Future.wait([output, errors]);
      fail('Owned preparation CLI exceeded its bounded wait.');
    },
  );
  final out = await output, err = await errors;
  stdout.writeln(
    'CLI executable=${dart.path} script=${script.path} cwd=${project.path} exit=$code',
  );
  expect(code, failure == null ? 0 : 1, reason: '$out\n$err');
  if (failure == null) {
    expect(err, isEmpty);
  } else {
    expect(err, contains('Chrome test assets not prepared:'));
    expect(err, contains(failure));
  }
}

File _output(Directory project, String name) =>
    File('${project.path}/test/assets/shaders/$name');

void _noOutputs(Directory project) {
  for (final name in _names) {
    expect(
      FileSystemEntity.typeSync(
        _output(project, name).path,
        followLinks: false,
      ),
      FileSystemEntityType.notFound,
    );
  }
}

Directory _actualSDK() {
  final config = File('.dart_tool/package_config.json').absolute;
  final packages =
      (jsonDecode(config.readAsStringSync()) as Map)['packages'] as List;
  final entry = packages.singleWhere((p) => p['name'] == 'flutter');
  return Directory(
    Directory.fromUri(
      config.uri.resolve(entry['rootUri'] as String),
    ).resolveSymbolicLinksSync(),
  ).parent.parent;
}

Directory _project() {
  final project = Directory.systemTemp.createTempSync('chrome-assets-project.');
  addTearDown(() => project.deleteSync(recursive: true));
  for (final path in ['tool', 'test', '.dart_tool']) {
    Directory('${project.path}/$path').createSync();
  }
  File('${project.path}/pubspec.yaml').writeAsStringSync(
    'name: vibermate_app\ndependencies:\n  flutter:\n    sdk: flutter\n',
  );
  File('tool/flutter-sdk.env').copySync('${project.path}/tool/flutter-sdk.env');
  _selectSDK(project, _actualSDK());
  return project;
}

void _selectSDK(Directory project, Directory sdk) {
  File('${project.path}/.dart_tool/package_config.json').writeAsStringSync(
    jsonEncode({
      'packages': [
        {'name': 'flutter', 'rootUri': '${sdk.uri}packages/flutter/'},
      ],
    }),
  );
}

Future<void> _privateSDK(Directory project, String failure) async {
  final actual = _actualSDK();
  final sdk = Directory('${project.path}/synthetic-sdk')..createSync();
  // Read-only git identity points at the pinned checkout. Every injected file,
  // including the failing executable, lives exclusively in this private copy.
  final gitDir = await Process.run('git', [
    '-C',
    actual.path,
    'rev-parse',
    '--absolute-git-dir',
  ]);
  expect(gitDir.exitCode, 0);
  File(
    '${sdk.path}/.git',
  ).writeAsStringSync('gitdir: ${gitDir.stdout.toString().trim()}\n');
  final cache = Directory('${sdk.path}/bin/cache')..createSync(recursive: true);
  File(
    '${actual.path}/bin/cache/flutter.version.json',
  ).copySync('${cache.path}/flutter.version.json');
  if (failure == 'wrong SDK') {
    final file = File('${cache.path}/flutter.version.json');
    final metadata =
        jsonDecode(file.readAsStringSync()) as Map<String, dynamic>;
    metadata['frameworkVersion'] = '0.0.0';
    file.writeAsStringSync(jsonEncode(metadata));
  }
  for (final name in _names) {
    final destination = File(
      '${sdk.path}/packages/flutter/lib/src/material/shaders/$name',
    );
    destination.parent.createSync(recursive: true);
    File(
      '${actual.path}/packages/flutter/lib/src/material/shaders/$name',
    ).copySync(destination.path);
  }
  final machine = Platform.isLinux
      ? (await Process.run('uname', ['-m'])).stdout.toString().trim()
      : '';
  final host = Platform.isMacOS
      ? 'darwin-x64'
      : machine == 'x86_64'
      ? 'linux-x64'
      : 'linux-arm64';
  final compiler = File('${cache.path}/artifacts/engine/$host/impellerc');
  Directory('${compiler.parent.path}/shader_lib').createSync(recursive: true);
  if (failure != 'missing compiler') {
    compiler.writeAsStringSync(
      failure == 'compiler exit'
          ? '#!/bin/sh\necho synthetic-compiler-failure >&2\nexit 17\n'
          : r'''#!/bin/sh
for arg do
  case "$arg" in --sl=*) output=${arg#--sl=} ;; esac
done
printf '{}\n' > "$output"
''',
    );
    expect((await Process.run('chmod', ['700', compiler.path])).exitCode, 0);
  }
  _selectSDK(project, sdk);
}
