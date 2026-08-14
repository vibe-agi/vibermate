import 'dart:convert';

import 'package:flutter/services.dart';

enum WorkbenchSection {
  captures('captures'),
  conversations('conversations'),
  environments('environments'),
  routes('routes'),
  network('network'),
  settings('settings');

  const WorkbenchSection(this.wireName);

  final String wireName;

  static WorkbenchSection? fromWire(Object? value) {
    for (final section in values) {
      if (section.wireName == value) return section;
    }
    return null;
  }
}

enum AppLanguage {
  english('en-US'),
  simplifiedChinese('zh-CN');

  const AppLanguage(this.wireName);

  final String wireName;

  static AppLanguage? fromWire(Object? value) {
    for (final language in values) {
      if (language.wireName == value) return language;
    }
    return null;
  }
}

enum WorkbenchTheme {
  system('system'),
  light('light'),
  dark('dark');

  const WorkbenchTheme(this.wireName);

  final String wireName;

  static WorkbenchTheme? fromWire(Object? value) {
    for (final theme in values) {
      if (theme.wireName == value) return theme;
    }
    return null;
  }
}

enum WorkbenchPreferencesIssue {
  corrupt('preferences.warning.corrupt'),
  futureSchema('preferences.warning.future_schema'),
  unavailable('preferences.warning.unavailable'),
  saveFailed('preferences.warning.save_failed');

  const WorkbenchPreferencesIssue(this.copyKey);

  final String copyKey;
}

final class WorkbenchPreferencesException implements Exception {
  const WorkbenchPreferencesException(this.message);

  final String message;

  @override
  String toString() => 'Workbench preferences error: $message';
}

final class WorkbenchPreferencesFutureSchema implements Exception {
  const WorkbenchPreferencesFutureSchema();
}

final class WorkbenchPreferences {
  const WorkbenchPreferences({
    this.language = AppLanguage.english,
    this.theme = WorkbenchTheme.system,
    this.section = WorkbenchSection.captures,
    this.selectedCaptureKey,
    this.selectedConversationKey,
    this.selectedEnvironmentId,
    this.selectedEnvironmentRevision,
    this.selectedEndpointId,
  });

  factory WorkbenchPreferences.decode(String encoded) {
    if (encoded.isEmpty || utf8.encode(encoded).length > maximumEncodedBytes) {
      throw const WorkbenchPreferencesException(
        'encoded preferences are empty or too large',
      );
    }
    final Object? decoded;
    try {
      decoded = jsonDecode(encoded);
    } on FormatException {
      throw const WorkbenchPreferencesException(
        'encoded preferences are not JSON',
      );
    }
    if (decoded is! Map) {
      throw const WorkbenchPreferencesException(
        'encoded preferences are not an object',
      );
    }
    final value = <String, Object?>{};
    for (final entry in decoded.entries) {
      if (entry.key is! String) {
        throw const WorkbenchPreferencesException(
          'preference field names must be strings',
        );
      }
      value[entry.key as String] = entry.value;
    }
    final observedSchema = value['schema'];
    if (observedSchema is String && observedSchema != schema) {
      throw const WorkbenchPreferencesFutureSchema();
    }
    if (observedSchema != schema ||
        value.keys.toSet().difference(_fields).isNotEmpty ||
        !_fields.every(value.containsKey)) {
      throw const WorkbenchPreferencesException(
        'preference fields do not match the contract',
      );
    }
    final language = AppLanguage.fromWire(value['language']);
    final theme = WorkbenchTheme.fromWire(value['theme']);
    final section = WorkbenchSection.fromWire(value['section']);
    final captureKey = _optionalSelection(
      value['selectedCaptureKey'],
      prefixes: const {'managed_run:', 'manual_capture:'},
    );
    final conversationKey = _optionalSelection(
      value['selectedConversationKey'],
      prefixes: const {'capture_run:', 'exchange:'},
    );
    final environmentId = _optionalResourceId(value['selectedEnvironmentId']);
    final environmentRevision = _optionalPositiveInteger(
      value['selectedEnvironmentRevision'],
    );
    final endpointId = _optionalResourceId(value['selectedEndpointId']);
    if (language == null ||
        theme == null ||
        section == null ||
        captureKey == _invalidSelection ||
        conversationKey == _invalidSelection ||
        environmentId == _invalidSelection ||
        environmentRevision == _invalidInteger ||
        environmentRevision != null && environmentId == null ||
        endpointId == _invalidSelection) {
      throw const WorkbenchPreferencesException(
        'preference values do not match the contract',
      );
    }
    return WorkbenchPreferences(
      language: language,
      theme: theme,
      section: section,
      selectedCaptureKey: captureKey,
      selectedConversationKey: conversationKey,
      selectedEnvironmentId: environmentId,
      selectedEnvironmentRevision: environmentRevision,
      selectedEndpointId: endpointId,
    );
  }

  static const schema = 'vibermate-workbench-preferences/v2';
  static const maximumEncodedBytes = 4096;
  static const _fields = {
    'schema',
    'language',
    'theme',
    'section',
    'selectedCaptureKey',
    'selectedConversationKey',
    'selectedEnvironmentId',
    'selectedEnvironmentRevision',
    'selectedEndpointId',
  };
  static const _invalidSelection = '\u0000';
  static const _invalidInteger = -1;
  static final _resourceId = RegExp(r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$');

  final AppLanguage language;
  final WorkbenchTheme theme;
  final WorkbenchSection section;
  final String? selectedCaptureKey;
  final String? selectedConversationKey;
  final String? selectedEnvironmentId;
  final int? selectedEnvironmentRevision;
  final String? selectedEndpointId;

  String encode() {
    final encoded = jsonEncode({
      'schema': schema,
      'language': language.wireName,
      'theme': theme.wireName,
      'section': section.wireName,
      'selectedCaptureKey': selectedCaptureKey,
      'selectedConversationKey': selectedConversationKey,
      'selectedEnvironmentId': selectedEnvironmentId,
      'selectedEnvironmentRevision': selectedEnvironmentRevision,
      'selectedEndpointId': selectedEndpointId,
    });
    if (utf8.encode(encoded).length > maximumEncodedBytes) {
      throw const WorkbenchPreferencesException(
        'encoded preferences are too large',
      );
    }
    return encoded;
  }

  static String? _optionalSelection(
    Object? value, {
    required Set<String> prefixes,
  }) {
    if (value == null) return null;
    if (value is! String || utf8.encode(value).length > 256) {
      return _invalidSelection;
    }
    for (final prefix in prefixes) {
      if (value.startsWith(prefix) &&
          _resourceId.hasMatch(value.substring(prefix.length))) {
        return value;
      }
    }
    return _invalidSelection;
  }

  static String? _optionalResourceId(Object? value) {
    if (value == null) return null;
    if (value is String && _resourceId.hasMatch(value)) return value;
    return _invalidSelection;
  }

  static int? _optionalPositiveInteger(Object? value) {
    if (value == null) return null;
    if (value is int && value >= 1) return value;
    return _invalidInteger;
  }

  @override
  bool operator ==(Object other) =>
      other is WorkbenchPreferences &&
      other.language == language &&
      other.theme == theme &&
      other.section == section &&
      other.selectedCaptureKey == selectedCaptureKey &&
      other.selectedConversationKey == selectedConversationKey &&
      other.selectedEnvironmentId == selectedEnvironmentId &&
      other.selectedEnvironmentRevision == selectedEnvironmentRevision &&
      other.selectedEndpointId == selectedEndpointId;

  @override
  int get hashCode => Object.hash(
    language,
    theme,
    section,
    selectedCaptureKey,
    selectedConversationKey,
    selectedEnvironmentId,
    selectedEnvironmentRevision,
    selectedEndpointId,
  );
}

abstract interface class WorkbenchPreferencesStore {
  Future<String?> read();

  Future<void> write(String encoded);
}

final class PlatformWorkbenchPreferencesStore
    implements WorkbenchPreferencesStore {
  const PlatformWorkbenchPreferencesStore();

  static const _channel = MethodChannel('io.vibermate.desktop/preferences');

  @override
  Future<String?> read() async {
    final value = await _channel.invokeMethod<Object?>(
      'readWorkbenchPreferences',
    );
    if (value == null || value is String) return value as String?;
    throw const WorkbenchPreferencesException(
      'platform preferences returned an invalid value',
    );
  }

  @override
  Future<void> write(String encoded) async {
    // Validate again at the platform boundary so this store can never become a
    // generic persistence channel for credentials or arbitrary application data.
    WorkbenchPreferences.decode(encoded);
    await _channel.invokeMethod<void>('writeWorkbenchPreferences', encoded);
  }
}

final class MemoryWorkbenchPreferencesStore
    implements WorkbenchPreferencesStore {
  MemoryWorkbenchPreferencesStore({String? encoded}) : _encoded = encoded;

  String? _encoded;

  String? get encoded => _encoded;

  @override
  Future<String?> read() async => _encoded;

  @override
  Future<void> write(String encoded) async {
    WorkbenchPreferences.decode(encoded);
    _encoded = encoded;
  }
}

final class DiscardWorkbenchPreferencesStore
    implements WorkbenchPreferencesStore {
  const DiscardWorkbenchPreferencesStore();

  @override
  Future<String?> read() async => null;

  @override
  Future<void> write(String encoded) async {
    WorkbenchPreferences.decode(encoded);
  }
}

final class LoadedWorkbenchPreferences {
  const LoadedWorkbenchPreferences({
    required this.value,
    required this.writable,
    this.issue,
  });

  final WorkbenchPreferences value;
  final bool writable;
  final WorkbenchPreferencesIssue? issue;
}

Future<LoadedWorkbenchPreferences> loadWorkbenchPreferences(
  WorkbenchPreferencesStore store, {
  required AppLanguage fallbackLanguage,
}) async {
  final fallback = WorkbenchPreferences(language: fallbackLanguage);
  try {
    final encoded = await store.read();
    if (encoded == null) {
      return LoadedWorkbenchPreferences(value: fallback, writable: true);
    }
    return LoadedWorkbenchPreferences(
      value: WorkbenchPreferences.decode(encoded),
      writable: true,
    );
  } on WorkbenchPreferencesFutureSchema {
    return LoadedWorkbenchPreferences(
      value: fallback,
      writable: false,
      issue: WorkbenchPreferencesIssue.futureSchema,
    );
  } on WorkbenchPreferencesException {
    return LoadedWorkbenchPreferences(
      value: fallback,
      writable: true,
      issue: WorkbenchPreferencesIssue.corrupt,
    );
  } on Object {
    return LoadedWorkbenchPreferences(
      value: fallback,
      writable: true,
      issue: WorkbenchPreferencesIssue.unavailable,
    );
  }
}
