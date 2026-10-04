import 'dart:convert';
import 'control_models.dart';

enum NativeDecision {
  allowOnce('allow-once'),
  deny('deny');

  const NativeDecision(this.wire);
  final String wire;
}

void validateNativeCapture(String value) {
  if (!RegExp(
    r'^(managed_run|manual_capture):[A-Za-z0-9][A-Za-z0-9._-]{0,127}$',
  ).hasMatch(value)) {
    throw const ControlContractException('Invalid native Capture');
  }
}

String nativeHex(Object? value, int length) {
  if (value is! String || !RegExp('^[0-9a-f]{$length}\$').hasMatch(value)) {
    throw const ControlContractException('Invalid native identity');
  }
  return value;
}

final class NativeEnvironment {
  NativeEnvironment.fromJson(Object? raw) {
    final map = requireObject(raw, 'native.environment');
    requireFields(
      map,
      'native.environment',
      required: const {'id', 'revision', 'digest'},
    );
    id = requireString(map, 'id', 'native.environment');
    revision = requireInteger(map, 'revision', 'native.environment');
    digest = nativeHex(map['digest'], 64);
    if (!RegExp(r'^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$').hasMatch(id) ||
        revision < 1) {
      throw const ControlContractException('Invalid native Environment');
    }
  }
  late final String id, digest;
  late final int revision;
  bool same(NativeEnvironment other) =>
      id == other.id && revision == other.revision && digest == other.digest;
}

final class NativeIntentPreview {
  NativeIntentPreview.fromJson(Object? raw) {
    final map = requireObject(raw, 'native.intent');
    requireFields(
      map,
      'native.intent',
      required: const {'toolName', 'action', 'target', 'truncated'},
      optional: const {'arguments'},
    );
    toolName = requireString(map, 'toolName', 'native.intent');
    action = requireString(map, 'action', 'native.intent');
    target = requireString(map, 'target', 'native.intent');
    if (map['truncated'] is! bool) {
      throw const ControlContractException('Invalid native truncation');
    }
    truncated = map['truncated'] as bool;
    final origin = Uri.tryParse(target);
    if (origin == null ||
        origin.scheme != 'https' ||
        origin.host.isEmpty ||
        origin.userInfo.isNotEmpty ||
        origin.path.isNotEmpty ||
        origin.hasQuery ||
        origin.hasFragment) {
      throw const ControlContractException('Invalid native target origin');
    }
    if (action == 'tool_call') {
      if (toolName.isEmpty || (!map.containsKey('arguments') && !truncated)) {
        throw const ControlContractException('Missing frozen tool intent');
      }
      arguments = map.containsKey('arguments')
          ? jsonEncode(requireObject(map['arguments'], 'native.arguments'))
          : null;
    } else {
      if (!const {
            'form_reply',
            'url_reply',
            'verification_reply',
          }.contains(action) ||
          toolName.isNotEmpty ||
          map.containsKey('arguments') ||
          truncated) {
        throw const ControlContractException(
          'Invalid registered reply summary',
        );
      }
      arguments = null;
    }
  }
  late final String toolName, action, target;
  late final String? arguments;
  late final bool truncated;
}

final class NativeApproval {
  NativeApproval.fromJson(Object? raw, String expectedCapture) {
    validateNativeCapture(expectedCapture);
    final map = requireObject(raw, 'native.approval');
    requireFields(
      map,
      'native.approval',
      required: const {
        'id',
        'operationId',
        'captureKey',
        'environment',
        'revision',
        'state',
        'expiresAt',
        'bindingDigest',
        'previewState',
        'credentialOwner',
      },
      optional: const {'preview'},
    );
    id = nativeHex(map['id'], 32);
    operationId = nativeHex(map['operationId'], 32);
    bindingDigest = nativeHex(map['bindingDigest'], 64);
    captureKey = requireString(map, 'captureKey', 'native.approval');
    environment = NativeEnvironment.fromJson(map['environment']);
    revision = requireInteger(map, 'revision', 'native.approval');
    state = requireString(map, 'state', 'native.approval');
    previewState = requireString(map, 'previewState', 'native.approval');
    credentialOwner = requireString(map, 'credentialOwner', 'native.approval');
    final stamp = requireString(map, 'expiresAt', 'native.approval');
    final parsed = DateTime.tryParse(stamp);
    if (captureKey != expectedCapture ||
        revision < 1 ||
        parsed == null ||
        !RegExp(r'T.*(Z|[+-]\d\d:\d\d)$').hasMatch(stamp) ||
        !const {
          'pending',
          'allowed',
          'denied',
          'canceled',
          'expired',
        }.contains(state) ||
        !const {'available', 'unavailable', 'expired'}.contains(previewState) ||
        credentialOwner != 'original_client') {
      throw const ControlContractException('Invalid native approval authority');
    }
    expiresAt = parsed.toUtc();
    if (map.containsKey('preview')) {
      final preview = requireObject(map['preview'], 'native.preview');
      requireFields(
        preview,
        'native.preview',
        required: const {'intents', 'truncated'},
      );
      if (state != 'pending' ||
          previewState != 'available' ||
          preview['truncated'] is! bool ||
          utf8.encode(jsonEncode(preview)).length > (66 << 10)) {
        throw const ControlContractException('Invalid native preview');
      }
      intents = List.unmodifiable(
        requireList(
          preview['intents'],
          'native.intents',
        ).map(NativeIntentPreview.fromJson),
      );
      if (intents.isEmpty ||
          intents.length > 128 ||
          intents.any((i) => i.truncated) != preview['truncated']) {
        throw const ControlContractException('Invalid native preview group');
      }
    } else {
      if (previewState == 'available') {
        throw const ControlContractException('Missing native preview');
      }
      intents = const [];
    }
  }
  late final String id,
      operationId,
      captureKey,
      bindingDigest,
      state,
      previewState,
      credentialOwner;
  late final NativeEnvironment environment;
  late final int revision;
  late final DateTime expiresAt;
  late final List<NativeIntentPreview> intents;
  bool get pending =>
      state == 'pending' && expiresAt.isAfter(DateTime.now().toUtc());
  bool get canAllow =>
      pending && previewState == 'available' && intents.isNotEmpty;
  bool sameIntent(NativeApproval other) =>
      id == other.id &&
      operationId == other.operationId &&
      captureKey == other.captureKey &&
      bindingDigest == other.bindingDigest &&
      environment.same(other.environment) &&
      credentialOwner == other.credentialOwner;
}

final class NativeApprovalPage {
  const NativeApprovalPage(this.items, this.nextSequence);
  factory NativeApprovalPage.fromJson(
    Object? raw,
    String capture, {
    required int afterSequence,
    required int limit,
  }) {
    final map = requireObject(raw, 'native.approvals');
    requireFields(
      map,
      'native.approvals',
      required: const {'items', 'nextSequence'},
    );
    final sequence = requireInteger(map, 'nextSequence', 'native.approvals');
    final items = requireList(map['items'], 'native.approvals.items')
        .map((item) => NativeApproval.fromJson(item, capture))
        .toList(growable: false);
    if (sequence < afterSequence ||
        items.length > limit ||
        items.any((a) => a.state != 'pending') ||
        items.map((a) => a.id).toSet().length != items.length) {
      throw const ControlContractException('Invalid native approval page');
    }
    return NativeApprovalPage(List.unmodifiable(items), sequence);
  }
  final List<NativeApproval> items;
  final int nextSequence;
}
