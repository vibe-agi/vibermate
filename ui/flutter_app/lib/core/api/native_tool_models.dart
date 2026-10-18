import 'dart:convert';
import 'dart:typed_data';
import 'control_models.dart';

final class NativeToolOperationPage {
  const NativeToolOperationPage(
    this.items,
    this.nextCursor,
    this.inspectionVersion,
  );
  factory NativeToolOperationPage.fromJson(
    Object? raw,
    String capture,
    String after,
    int limit,
  ) {
    final map = requireObject(raw, 'native.operations');
    requireFields(
      map,
      'native.operations',
      required: const {'items', 'nextCursor', 'inspectionVersion'},
    );
    final version = nativeHex(map['inspectionVersion'], 64);
    final items = requireList(
      map['items'],
      'native.operations.items',
    ).map((item) => NativeToolOperation.fromJson(item, capture)).toList();
    final cursor = requireString(map, 'nextCursor', 'native.operations');
    if (cursor.isEmpty ||
        utf8.encode(cursor).length > 512 ||
        items.length > limit ||
        items.map((op) => op.operationId).toSet().length != items.length ||
        items.any((op) => op.inspectionVersion != version)) {
      throw const ControlContractException('Invalid native operation page');
    }
    return NativeToolOperationPage(List.unmodifiable(items), cursor, version);
  }
  final List<NativeToolOperation> items;
  final String nextCursor, inspectionVersion;
}

DateTime _nativeTime(Object? raw) {
  if (raw is! String || !RegExp(r'T.*(Z|[+-]\d\d:\d\d)$').hasMatch(raw)) {
    throw const ControlContractException('Invalid native time');
  }
  final value = DateTime.tryParse(raw);
  if (value == null) {
    throw const ControlContractException('Invalid native time');
  }
  return value.toUtc();
}

final class NativePartDescriptor {
  NativePartDescriptor.fromJson(Object? raw) {
    final map = requireObject(raw, 'native.part');
    requireFields(
      map,
      'native.part',
      required: const {'state'},
      optional: const {'digest', 'expiresAt', 'observedAt'},
    );
    state = requireString(map, 'state', 'native.part');
    observedAt = map['observedAt'] == null
        ? null
        : _nativeTime(map['observedAt']);
    if (!const {
      'off',
      'metadata_only',
      'available',
      'expired',
      'unavailable',
    }.contains(state)) {
      throw const ControlContractException('Invalid native part state');
    }
    digest = map.containsKey('digest') ? nativeHex(map['digest'], 64) : null;
    expiresAt = map.containsKey('expiresAt')
        ? _nativeTime(map['expiresAt'])
        : null;
    if ((state == 'available' || state == 'expired') !=
            (digest != null && expiresAt != null) ||
        (digest == null) != (expiresAt == null)) {
      throw const ControlContractException('Invalid native part descriptor');
    }
  }
  late final String state;
  late final String? digest;
  late final DateTime? expiresAt, observedAt;
}

final class NativeToolOperation {
  NativeToolOperation.fromJson(Object? raw, String expectedCapture) {
    validateNativeCapture(expectedCapture);
    final map = requireObject(raw, 'native.operation');
    requireFields(
      map,
      'native.operation',
      required: const {
        'inspectionVersion',
        'operationId',
        'captureKey',
        'environment',
        'revision',
        'kind',
        'toolName',
        'state',
        'reason',
        'decisionSource',
        'targetOrigin',
        'credentialOwner',
        'createdAt',
        'completedAt',
        'bytesIn',
        'bytesOut',
        'content',
      },
    );
    inspectionVersion = nativeHex(map['inspectionVersion'], 64);
    operationId = nativeHex(map['operationId'], 32);
    captureKey = requireString(map, 'captureKey', 'native.operation');
    environment = NativeEnvironment.fromJson(map['environment']);
    revision = requireInteger(map, 'revision', 'native.operation');
    kind = requireString(map, 'kind', 'native.operation');
    toolName = requireStringValue(map, 'toolName', 'native.operation');
    state = requireString(map, 'state', 'native.operation');
    reason = requireStringValue(map, 'reason', 'native.operation');
    decisionSource = requireStringValue(
      map,
      'decisionSource',
      'native.operation',
    );
    targetOrigin = requireString(map, 'targetOrigin', 'native.operation');
    credentialOwner = requireStringValue(
      map,
      'credentialOwner',
      'native.operation',
    );
    createdAt = _nativeTime(map['createdAt']);
    completedAt = map['completedAt'] == null
        ? null
        : _nativeTime(map['completedAt']);
    int? counter(String key) {
      final value = map[key];
      if (value == null) {
        return null;
      }
      if (value is! int || value < 0) {
        throw const ControlContractException('Invalid native counter');
      }
      return value;
    }

    bytesIn = counter('bytesIn');
    bytesOut = counter('bytesOut');
    final origin = Uri.tryParse(targetOrigin);
    if (captureKey != expectedCapture ||
        (state == 'unknown' ? revision != 0 : revision < 1) ||
        !RegExp(r'^[A-Za-z0-9_.:/-]{0,256}$').hasMatch(toolName) ||
        !const {
          'unknown',
          'received',
          'validated',
          'awaiting_decision',
          'started',
          'completed',
          'denied',
          'failed',
          'canceled',
          'uncertain',
        }.contains(state) ||
        (state == 'unknown'
            ? decisionSource.isNotEmpty
            : !const {'none', 'policy', 'user'}.contains(decisionSource)) ||
        credentialOwner != '' && credentialOwner != 'original_client' ||
        origin == null ||
        origin.scheme != 'https' ||
        origin.host.isEmpty ||
        origin.path.isNotEmpty ||
        origin.userInfo.isNotEmpty ||
        origin.hasQuery ||
        origin.hasFragment ||
        completedAt != null && completedAt!.isBefore(createdAt)) {
      throw const ControlContractException('Invalid native operation');
    }
    final terminal = const {
      'completed',
      'denied',
      'failed',
      'canceled',
      'uncertain',
    }.contains(state);
    if (!const {
          'unclassified',
          'initialize',
          'initialized',
          'tools_list',
          'tools_call',
          'ping',
          'cancel',
          'server_reply',
          'get_stream',
          'delete_session',
        }.contains(kind) ||
        !const {
          '',
          'permission_off',
          'policy_strict',
          'approval_denied',
          'approval_expired',
          'revoked',
          'canceled',
          'transport_failed',
          'rpc_error',
          'execution_uncertain',
          'completed',
        }.contains(reason)) {
      throw const ControlContractException(
        'Invalid native operation kind or reason',
      );
    }
    if (terminal != (completedAt != null) ||
        terminal == reason.isEmpty ||
        state == 'unknown' && (bytesIn != null || bytesOut != null)) {
      throw const ControlContractException('Invalid native lifecycle');
    }
    final content = requireObject(map['content'], 'native.content');
    requireFields(
      content,
      'native.content',
      required: const {'mode', 'arguments', 'result'},
    );
    mode = requireString(content, 'mode', 'native.content');
    arguments = NativePartDescriptor.fromJson(content['arguments']);
    result = NativePartDescriptor.fromJson(content['result']);
    if (!const {'off', 'metadata_only', 'full'}.contains(mode) ||
        mode != 'full' && (arguments.state != mode || result.state != mode)) {
      throw const ControlContractException('Invalid native recording');
    }
  }
  late final String inspectionVersion,
      operationId,
      captureKey,
      kind,
      toolName,
      state,
      reason,
      decisionSource,
      targetOrigin,
      credentialOwner,
      mode;
  late final NativeEnvironment environment;
  late final int revision;
  late final DateTime createdAt;
  late final DateTime? completedAt;
  late final int? bytesIn, bytesOut;
  late final NativePartDescriptor arguments, result;
  NativePartDescriptor part(String part) =>
      part == 'tool_arguments' ? arguments : result;
}

final class NativeContentBlock {
  NativeContentBlock.fromJson(Object? raw) {
    final map = requireObject(raw, 'native.block');
    requireFields(
      map,
      'native.block',
      required: const {'kind', 'offset', 'total', 'complete'},
      optional: const {'text', 'mimeType', 'imageData'},
    );
    kind = requireString(map, 'kind', 'native.block');
    offset = requireInteger(map, 'offset', 'native.block');
    total = requireInteger(map, 'total', 'native.block');
    if (map['complete'] is! bool) {
      throw const ControlContractException('Invalid native fragment');
    }
    complete = map['complete'] as bool;
    if (kind == 'image') {
      if (map.containsKey('text') ||
          !const {
            'image/png',
            'image/jpeg',
            'image/webp',
          }.contains(map['mimeType'])) {
        throw const ControlContractException('Invalid native image');
      }
      mimeType = map['mimeType'] as String;
      value = requireString(map, 'imageData', 'native.block');
      if (value.length % 4 != 0 ||
          !RegExp(r'^[A-Za-z0-9+/]*={0,2}$').hasMatch(value) ||
          !complete && value.contains('=')) {
        throw const ControlContractException('Invalid image fragment');
      }
    } else {
      if (!const {'text', 'json'}.contains(kind) ||
          map.containsKey('mimeType') ||
          map.containsKey('imageData')) {
        throw const ControlContractException('Invalid native block');
      }
      mimeType = null;
      value = requireString(map, 'text', 'native.block');
    }
    final size = utf8.encode(value).length;
    if (offset < 0 ||
        total < 1 ||
        total > 24 << 20 ||
        size == 0 ||
        offset + size > total ||
        complete != (offset + size == total)) {
      throw const ControlContractException('Invalid native fragment bounds');
    }
  }
  late final String kind, value;
  late final String? mimeType;
  late final int offset, total;
  late final bool complete;
}

final class NativeToolContentPage {
  NativeToolContentPage.fromJson(
    Object? raw,
    String capture,
    String operation,
    String requestedPart,
  ) {
    final map = requireObject(raw, 'native.contentPage');
    requireFields(
      map,
      'native.contentPage',
      required: const {
        'inspectionVersion',
        'operationId',
        'captureKey',
        'part',
        'bodyDigest',
        'blocks',
      },
      optional: const {'nextCursor'},
    );
    inspectionVersion = nativeHex(map['inspectionVersion'], 64);
    operationId = nativeHex(map['operationId'], 32);
    captureKey = requireString(map, 'captureKey', 'native.contentPage');
    part = requireString(map, 'part', 'native.contentPage');
    bodyDigest = nativeHex(map['bodyDigest'], 64);
    blocks = List.unmodifiable(
      requireList(
        map['blocks'],
        'native.blocks',
      ).map(NativeContentBlock.fromJson),
    );
    nextCursor = map.containsKey('nextCursor')
        ? requireString(map, 'nextCursor', 'native.contentPage')
        : null;
    if (operationId != operation ||
        captureKey != capture ||
        part != requestedPart ||
        blocks.length > 24 ||
        nextCursor != null &&
            (nextCursor!.isEmpty || utf8.encode(nextCursor!).length > 512) ||
        utf8.encode(jsonEncode(map)).length > 128 << 10 ||
        blocks.isEmpty && nextCursor != null) {
      throw const ControlContractException('Invalid native content page');
    }
  }
  late final String inspectionVersion,
      operationId,
      captureKey,
      part,
      bodyDigest;
  late final List<NativeContentBlock> blocks;
  late final String? nextCursor;
}

final class NativeRecordedBlock {
  NativeRecordedBlock(this.kind, String source, this.mimeType)
    : value = kind == 'image' ? '' : source {
    if (kind == 'json') {
      final decoded = jsonDecode(value);
      if (decoded is! Map) {
        throw const ControlContractException('Invalid complete native JSON');
      }
    }
    if (kind == 'image') {
      imageBytes = base64Decode(source).asUnmodifiableView();
      if (imageBytes!.isEmpty ||
          imageBytes!.length > 4 << 20 ||
          base64Encode(imageBytes!) != source ||
          !_nativeImageDimensions(imageBytes!, mimeType!)) {
        throw const ControlContractException('Invalid complete native image');
      }
    }
  }
  final String kind, value;
  final String? mimeType;
  Uint8List? imageBytes;
  int get byteSize => imageBytes?.length ?? utf8.encode(value).length;
}

bool _nativeImageDimensions(Uint8List data, String mime) {
  final view = ByteData.sublistView(data);
  int width = 0, height = 0;
  bool signature(int start, List<int> bytes) =>
      data.length >= start + bytes.length &&
      List.generate(
        bytes.length,
        (i) => data[start + i] == bytes[i],
      ).every((v) => v);
  if (mime == 'image/png') {
    if (data.length < 33 ||
        !signature(0, [137, 80, 78, 71, 13, 10, 26, 10]) ||
        !signature(12, [73, 72, 68, 82])) {
      return false;
    }
    width = view.getUint32(16);
    height = view.getUint32(20);
  } else if (mime == 'image/jpeg') {
    if (!signature(0, [255, 216]) || !signature(data.length - 2, [255, 217])) {
      return false;
    }
    for (var i = 2; i + 4 < data.length;) {
      if (data[i++] != 255) {
        return false;
      }
      while (i < data.length && data[i] == 255) {
        i++;
      }
      if (i >= data.length) {
        return false;
      }
      final marker = data[i++];
      if (marker == 216 || marker == 1 || marker >= 208 && marker <= 215) {
        continue;
      }
      if (i + 2 > data.length) {
        return false;
      }
      final size = view.getUint16(i);
      if (size < 2 || i + size > data.length) {
        return false;
      }
      if (const {
        192,
        193,
        194,
        195,
        197,
        198,
        199,
        201,
        202,
        203,
        205,
        206,
        207,
      }.contains(marker)) {
        if (size < 8) {
          return false;
        }
        height = view.getUint16(i + 3);
        width = view.getUint16(i + 5);
        break;
      }
      if (marker == 218 || marker == 217) {
        return false;
      }
      i += size;
    }
  } else if (mime == 'image/webp') {
    if (data.length < 30 ||
        !signature(0, [82, 73, 70, 70]) ||
        !signature(8, [87, 69, 66, 80]) ||
        view.getUint32(4, Endian.little) + 8 != data.length) {
      return false;
    }
    int? canvasWidth, canvasHeight;
    for (var i = 12; i + 8 <= data.length;) {
      final size = view.getUint32(i + 4, Endian.little);
      final start = i + 8;
      if (start + size > data.length) {
        return false;
      }
      if (signature(i, [86, 80, 56, 88])) {
        if (size != 10) {
          return false;
        }
        canvasWidth =
            1 +
            data[start + 4] +
            (data[start + 5] << 8) +
            (data[start + 6] << 16);
        canvasHeight =
            1 +
            data[start + 7] +
            (data[start + 8] << 8) +
            (data[start + 9] << 16);
      }
      if (signature(i, [86, 80, 56, 32])) {
        if (size < 10 || !signature(start + 3, [157, 1, 42])) {
          return false;
        }
        width = view.getUint16(start + 6, Endian.little) & 16383;
        height = view.getUint16(start + 8, Endian.little) & 16383;
        break;
      }
      if (signature(i, [86, 80, 56, 76])) {
        if (size < 5 || data[start] != 47) {
          return false;
        }
        final bits = view.getUint32(start + 1, Endian.little);
        width = 1 + (bits & 16383);
        height = 1 + ((bits >> 14) & 16383);
        break;
      }
      i = start + size + (size & 1);
    }
    if (canvasWidth != null &&
        (width != canvasWidth || height != canvasHeight)) {
      return false;
    }
  } else {
    return false;
  }
  return width > 0 && height > 0 && width <= (16 << 20) ~/ height;
}

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
    toolName = requireStringValue(map, 'toolName', 'native.intent');
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
            'ping_reply',
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
  const NativeApprovalPage(this.items, this.nextCursor);
  factory NativeApprovalPage.fromJson(
    Object? raw,
    String capture, {
    required String cursor,
    required int limit,
  }) {
    final map = requireObject(raw, 'native.approvals');
    requireFields(
      map,
      'native.approvals',
      required: const {'items', 'nextCursor'},
    );
    final next = requireStringValue(map, 'nextCursor', 'native.approvals');
    final items = requireList(map['items'], 'native.approvals.items')
        .map((item) => NativeApproval.fromJson(item, capture))
        .toList(growable: false);
    if (next.length > 256 ||
        (next.isNotEmpty &&
            (!RegExp(r'^[A-Za-z0-9_-]+$').hasMatch(next) || next == cursor)) ||
        items.length > limit ||
        items.any((a) => a.state != 'pending') ||
        items.map((a) => a.id).toSet().length != items.length) {
      throw const ControlContractException('Invalid native approval page');
    }
    return NativeApprovalPage(List.unmodifiable(items), next);
  }
  final List<NativeApproval> items;
  final String nextCursor;
}
