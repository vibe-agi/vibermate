import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

const _names = ['ink_sparkle.frag', 'stretch_effect.frag'];

Future<void> main(List<String> arguments) async {
  try {
    if (arguments.isNotEmpty) {
      throw StateError('This command takes no arguments.');
    }
    await prepareChromeTestAssets(Directory.current);
  } on Object catch (error) {
    stderr.writeln('Chrome test assets not prepared: $error');
    exitCode = 1;
  }
}

/// Prepares only the two pinned framework shaders for this Flutter project's
/// local browser-test server. Conflicting or linked outputs are never replaced.
Future<void> prepareChromeTestAssets(Directory project) async {
  _directory(project.path);
  project = Directory(project.resolveSymbolicLinksSync());
  final pubspec = File('${project.path}/pubspec.yaml');
  if (!pubspec.existsSync() ||
      !RegExp(
        r'^name: vibermate_app$',
        multiLine: true,
      ).hasMatch(pubspec.readAsStringSync()) ||
      !RegExp(
        r'^  flutter:\s*\n    sdk: flutter$',
        multiLine: true,
      ).hasMatch(pubspec.readAsStringSync())) {
    throw StateError('Expected the vibermate_app Flutter project.');
  }
  _directory('${project.path}/test');
  _directory('${project.path}/tool');
  _directory('${project.path}/.dart_tool');
  final pins = <String, String>{};
  for (final line in File(
    '${project.path}/tool/flutter-sdk.env',
  ).readAsLinesSync()) {
    if (line.startsWith('#') || line.trim().isEmpty) continue;
    final parts = line.split('=');
    if (parts.length != 2 || pins.containsKey(parts.first)) {
      throw StateError('Invalid Flutter pin file.');
    }
    pins[parts.first] = parts.last;
  }
  final version = pins['VIBERMATE_FLUTTER_VERSION'];
  final revision = pins['VIBERMATE_FLUTTER_REVISION'];
  if (version == null ||
      !RegExp(r'^\d+\.\d+\.\d+$').hasMatch(version) ||
      revision == null ||
      !RegExp(r'^[0-9a-f]{40}$').hasMatch(revision)) {
    throw StateError('Missing exact Flutter version/revision pins.');
  }
  final configFile = File('${project.path}/.dart_tool/package_config.json');
  final config =
      jsonDecode(configFile.readAsStringSync()) as Map<String, dynamic>;
  final packages = (config['packages'] as List)
      .where((p) => p['name'] == 'flutter')
      .toList();
  if (packages.length != 1) {
    throw StateError(
      'Expected one resolved package:flutter. Run the approved pub get first.',
    );
  }
  final packageURI = configFile.uri.resolve(
    packages.single['rootUri'] as String,
  );
  if (packageURI.scheme != 'file' ||
      packageURI.hasQuery ||
      packageURI.hasFragment) {
    throw StateError('Flutter package must be local.');
  }
  final framework = Directory(
    Directory.fromUri(packageURI).resolveSymbolicLinksSync(),
  );
  final sdk = framework.parent.parent;
  if (framework.path != '${sdk.path}/packages/flutter') {
    throw StateError('Unexpected Flutter package layout.');
  }
  final metadata =
      jsonDecode(
            File(
              '${sdk.path}/bin/cache/flutter.version.json',
            ).readAsStringSync(),
          )
          as Map<String, dynamic>;
  final head = await Process.run('git', ['-C', sdk.path, 'rev-parse', 'HEAD']);
  if (head.exitCode != 0 ||
      head.stdout.toString().trim() != revision ||
      metadata['frameworkVersion'] != version ||
      metadata['frameworkRevision'] != revision) {
    throw StateError('Resolved Flutter SDK does not match $version@$revision.');
  }
  final host = await _host();
  final compiler = File(
    '${sdk.path}/bin/cache/artifacts/engine/$host/impellerc',
  );
  if (!compiler.existsSync()) {
    throw StateError('Missing cached pinned impellerc: ${compiler.path}');
  }
  _directory('${compiler.parent.path}/shader_lib');
  final sources = [
    for (final name in _names)
      File('${framework.path}/lib/src/material/shaders/$name'),
  ];
  for (final source in sources) {
    if (!source.existsSync()) {
      throw StateError('Missing pinned framework shader: ${source.path}');
    }
  }
  // Reject existing links/types before compiling, and again before publishing.
  _outputParents(project, create: false);
  for (final name in _names) {
    _outputType('${project.path}/test/assets/shaders/$name');
  }
  final temporary = Directory.systemTemp.createTempSync(
    'vibermate-chrome-shaders.',
  );
  var success = false;
  try {
    final outputs = <Uint8List>[];
    for (var i = 0; i < _names.length; i++) {
      final output = File('${temporary.path}/${_names[i]}');
      final args = [
        '--sksl',
        '--iplr',
        '--json',
        '--sl=${output.path}',
        '--spirv=${output.path}.spirv',
        '--input=${sources[i].path}',
        '--input-type=frag',
        '--include=${sources[i].parent.path}',
        '--include=${compiler.parent.path}/shader_lib',
      ];
      stdout.writeln(
        jsonEncode({
          'sdk': sdk.path,
          'version': version,
          'revision': revision,
          'compiler': compiler.path,
          'argv': args,
        }),
      );
      final process = await Process.start(compiler.path, args);
      final out = process.stdout.transform(utf8.decoder).join();
      final err = process.stderr.transform(utf8.decoder).join();
      var timedOut = false;
      final code = await process.exitCode.timeout(
        const Duration(seconds: 30),
        onTimeout: () {
          timedOut = true;
          process.kill(ProcessSignal.sigkill);
          return process.exitCode;
        },
      );
      final diagnostics =
          'exit=$code timeout=$timedOut\n${await out}\n${await err}';
      File('${output.path}.compiler.log').writeAsStringSync(diagnostics);
      if (timedOut || code != 0) {
        throw StateError('impellerc failed for ${_names[i]}: $diagnostics');
      }
      final bytes = output.readAsBytesSync();
      _validate(bytes);
      outputs.add(bytes);
    }
    _outputParents(project, create: false);
    // Check BOTH conflicts before creating either target.
    for (var i = 0; i < _names.length; i++) {
      final file = File('${project.path}/test/assets/shaders/${_names[i]}');
      final type = _outputType(file.path);
      if (type == FileSystemEntityType.file &&
          !_same(file.readAsBytesSync(), outputs[i])) {
        throw StateError('Refusing conflicting existing shader: ${file.path}');
      }
    }
    _outputParents(project, create: true);
    for (var i = 0; i < _names.length; i++) {
      final file = File('${project.path}/test/assets/shaders/${_names[i]}');
      final type = _outputType(file.path);
      if (type == FileSystemEntityType.file) {
        if (!_same(file.readAsBytesSync(), outputs[i])) {
          throw StateError('Output changed during preparation: ${file.path}');
        }
        stdout.writeln('Unchanged ${file.path} (${outputs[i].length} bytes)');
      } else {
        file.createSync(exclusive: true);
        file.writeAsBytesSync(outputs[i], flush: true);
        stdout.writeln('Created ${file.path} (${outputs[i].length} bytes)');
      }
    }
    success = true;
  } on Object catch (error) {
    throw StateError('$error; retained compiler work at ${temporary.path}');
  } finally {
    // Only this invocation's private temporary directory is removed on success.
    if (success) temporary.deleteSync(recursive: true);
  }
}

Future<String> _host() async {
  if (Platform.isMacOS) return 'darwin-x64'; // Flutter TargetPlatform.darwin.
  if (Platform.isLinux) {
    final machine = await Process.run('uname', ['-m']);
    if (machine.exitCode == 0) {
      return switch (machine.stdout.toString().trim()) {
        'x86_64' => 'linux-x64',
        'aarch64' || 'arm64' => 'linux-arm64',
        _ => throw StateError('Unsupported Linux compiler architecture.'),
      };
    }
  }
  throw StateError('Only cached macOS/Linux Flutter compilers are supported.');
}

void _directory(String path) {
  if (FileSystemEntity.typeSync(path, followLinks: false) !=
      FileSystemEntityType.directory) {
    throw StateError('Expected a real directory, not a link: $path');
  }
}

void _outputParents(Directory project, {required bool create}) {
  for (final suffix in ['test', 'test/assets', 'test/assets/shaders']) {
    final path = '${project.path}/$suffix';
    final type = FileSystemEntity.typeSync(path, followLinks: false);
    if (type == FileSystemEntityType.notFound && create) {
      Directory(path).createSync();
    } else if (type != FileSystemEntityType.notFound) {
      _directory(path);
    }
  }
}

FileSystemEntityType _outputType(String path) {
  final type = FileSystemEntity.typeSync(path, followLinks: false);
  if (type != FileSystemEntityType.notFound &&
      type != FileSystemEntityType.file) {
    throw StateError('Refusing non-regular shader target: $path');
  }
  return type;
}

bool _same(List<int> a, List<int> b) {
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return false;
  }
  return true;
}

void _validate(Uint8List bytes) {
  final data = jsonDecode(utf8.decode(bytes));
  if (data is! Map || data['sksl'] is! Map) {
    throw StateError('Compiler output is not web shader JSON.');
  }
  final sksl = data['sksl'] as Map;
  if (sksl['shader'] is! String ||
      (sksl['shader'] as String).isEmpty ||
      sksl['uniforms'] is! List) {
    throw StateError('Compiler output lacks sksl shader/uniforms.');
  }
}
