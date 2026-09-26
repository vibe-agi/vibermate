import 'dart:convert';
import 'dart:typed_data';

import '../../core/api/control_models.dart';

enum RawEvidenceComparisonState {
  changed,
  identical,
  incomplete,
  unsupported,
  tooLarge,
}

enum RawEvidenceComparisonMode { json, text }

enum RawEvidenceChangeKind { added, removed, changed }

final class RawEvidenceChange {
  const RawEvidenceChange({
    required this.kind,
    required this.path,
    required this.before,
    required this.after,
  });

  final RawEvidenceChangeKind kind;
  final String path;
  final String? before;
  final String? after;

  Map<String, Object?> toJson() => {
    'kind': kind.name,
    'path': path,
    'before': before,
    'after': after,
  };
}

final class RawEvidenceComparison {
  const RawEvidenceComparison({
    required this.state,
    required this.mode,
    required this.leftLayer,
    required this.rightLayer,
    required this.leftDecoded,
    required this.rightDecoded,
    required this.changes,
    this.reason,
  });

  final RawEvidenceComparisonState state;
  final RawEvidenceComparisonMode? mode;
  final String leftLayer;
  final String rightLayer;
  final bool leftDecoded;
  final bool rightDecoded;
  final List<RawEvidenceChange> changes;
  final String? reason;

  String clipboardText() => const JsonEncoder.withIndent('  ').convert({
    'schema': 'vibermate.raw-evidence-diff/v1',
    'state': state.name,
    'mode': mode?.name,
    'left': {'layer': leftLayer, 'decoded': leftDecoded},
    'right': {'layer': rightLayer, 'decoded': rightDecoded},
    'reason': reason,
    'changes': changes.map((change) => change.toJson()).toList(growable: false),
  });
}

const _maximumComparedBodyBytes = 1024 * 1024;
const _maximumChanges = 5000;
const _maximumDisplayedValue = 2048;
const _missing = Object();

RawEvidenceComparison compareRawEvidence(
  RevealedRawEvidence left,
  RevealedRawEvidence right,
) {
  RawEvidenceComparison result(
    RawEvidenceComparisonState state, {
    RawEvidenceComparisonMode? mode,
    bool leftDecoded = false,
    bool rightDecoded = false,
    List<RawEvidenceChange> changes = const [],
    String? reason,
  }) => RawEvidenceComparison(
    state: state,
    mode: mode,
    leftLayer: left.envelope.layer,
    rightLayer: right.envelope.layer,
    leftDecoded: leftDecoded,
    rightDecoded: rightDecoded,
    changes: List.unmodifiable(changes),
    reason: reason,
  );

  if (!_complete(left) || !_complete(right)) {
    return result(
      RawEvidenceComparisonState.incomplete,
      reason: 'incomplete_evidence',
    );
  }
  final leftBody = _bodyView(left);
  final rightBody = _bodyView(right);
  if (leftBody.reason != null || rightBody.reason != null) {
    return result(
      RawEvidenceComparisonState.unsupported,
      leftDecoded: leftBody.decoded,
      rightDecoded: rightBody.decoded,
      reason: leftBody.reason ?? rightBody.reason,
    );
  }
  if (leftBody.bytes.length > _maximumComparedBodyBytes ||
      rightBody.bytes.length > _maximumComparedBodyBytes) {
    return result(
      RawEvidenceComparisonState.tooLarge,
      leftDecoded: leftBody.decoded,
      rightDecoded: rightBody.decoded,
      reason: 'body_too_large',
    );
  }
  late final String leftText;
  late final String rightText;
  try {
    leftText = utf8.decode(leftBody.bytes, allowMalformed: false);
    rightText = utf8.decode(rightBody.bytes, allowMalformed: false);
  } on FormatException {
    return result(
      RawEvidenceComparisonState.unsupported,
      leftDecoded: leftBody.decoded,
      rightDecoded: rightBody.decoded,
      reason: 'binary_body',
    );
  }
  final changes = <RawEvidenceChange>[];
  _diffFields(
    '/headers',
    _visibleFields(left.headers),
    _visibleFields(right.headers),
    changes,
  );
  _diffFields(
    '/trailers',
    _visibleFields(left.trailers),
    _visibleFields(right.trailers),
    changes,
  );
  if (changes.length >= _maximumChanges) {
    return result(
      RawEvidenceComparisonState.tooLarge,
      leftDecoded: leftBody.decoded,
      rightDecoded: rightBody.decoded,
      reason: 'too_many_changes',
    );
  }
  var mode = RawEvidenceComparisonMode.text;
  try {
    final leftJSON = jsonDecode(leftText);
    final rightJSON = jsonDecode(rightText);
    mode = RawEvidenceComparisonMode.json;
    if (!_diffJSON('/body', leftJSON, rightJSON, changes)) {
      return result(
        RawEvidenceComparisonState.tooLarge,
        mode: mode,
        leftDecoded: leftBody.decoded,
        rightDecoded: rightBody.decoded,
        reason: 'too_many_changes',
      );
    }
  } on FormatException {
    if (leftText != rightText) {
      changes.add(
        RawEvidenceChange(
          kind: RawEvidenceChangeKind.changed,
          path: '/body',
          before: _truncate(leftText),
          after: _truncate(rightText),
        ),
      );
    }
  }
  return result(
    changes.isEmpty
        ? RawEvidenceComparisonState.identical
        : RawEvidenceComparisonState.changed,
    mode: mode,
    leftDecoded: leftBody.decoded,
    rightDecoded: rightBody.decoded,
    changes: changes,
  );
}

bool _complete(RevealedRawEvidence value) =>
    value.envelope.payloadState == 'captured' &&
    value.envelope.digestScope == 'full_body' &&
    value.envelope.revealAvailable;

({Uint8List bytes, bool decoded, String? reason}) _bodyView(
  RevealedRawEvidence value,
) {
  final encoding = value.envelope.contentEncoding?.trim().toLowerCase() ?? '';
  if (encoding.isEmpty || encoding == 'identity') {
    return (bytes: value.body, decoded: false, reason: null);
  }
  final decoded = value.decodedBody;
  if (decoded?.state == 'decoded') {
    return (bytes: decoded!.body, decoded: true, reason: null);
  }
  return (
    bytes: Uint8List(0),
    decoded: false,
    reason: decoded?.state ?? 'unsupported_encoding',
  );
}

Map<String, List<String>> _visibleFields(List<RawHeaderField> fields) => {
  for (final field in fields)
    field.name.toLowerCase(): field.redacted.isEmpty
        ? List.unmodifiable(field.values)
        : List.filled(field.redacted.length, '[protected]'),
};

void _diffFields(
  String prefix,
  Map<String, List<String>> left,
  Map<String, List<String>> right,
  List<RawEvidenceChange> changes,
) {
  final names = {...left.keys, ...right.keys}.toList()..sort();
  for (final name in names) {
    final before = left[name];
    final after = right[name];
    if (before == null) {
      changes.add(
        RawEvidenceChange(
          kind: RawEvidenceChangeKind.added,
          path: '$prefix/${_pointer(name)}',
          before: null,
          after: _display(after),
        ),
      );
    } else if (after == null) {
      changes.add(
        RawEvidenceChange(
          kind: RawEvidenceChangeKind.removed,
          path: '$prefix/${_pointer(name)}',
          before: _display(before),
          after: null,
        ),
      );
    } else if (!_sameList(before, after)) {
      changes.add(
        RawEvidenceChange(
          kind: RawEvidenceChangeKind.changed,
          path: '$prefix/${_pointer(name)}',
          before: _display(before),
          after: _display(after),
        ),
      );
    }
  }
}

bool _diffJSON(
  String path,
  Object? left,
  Object? right,
  List<RawEvidenceChange> changes,
) {
  if (changes.length >= _maximumChanges) return false;
  if (left is Map && right is Map) {
    final leftMap = Map<String, Object?>.from(left);
    final rightMap = Map<String, Object?>.from(right);
    final keys = {...leftMap.keys, ...rightMap.keys}.toList()..sort();
    for (final key in keys) {
      if (!_diffJSON(
        '$path/${_pointer(key)}',
        leftMap.containsKey(key) ? leftMap[key] : _missing,
        rightMap.containsKey(key) ? rightMap[key] : _missing,
        changes,
      )) {
        return false;
      }
    }
    return true;
  }
  if (left is List && right is List) {
    final length = left.length > right.length ? left.length : right.length;
    for (var index = 0; index < length; index++) {
      if (!_diffJSON(
        '$path/$index',
        index < left.length ? left[index] : _missing,
        index < right.length ? right[index] : _missing,
        changes,
      )) {
        return false;
      }
    }
    return true;
  }
  if (identical(left, _missing)) {
    changes.add(
      RawEvidenceChange(
        kind: RawEvidenceChangeKind.added,
        path: path,
        before: null,
        after: _display(right),
      ),
    );
  } else if (identical(right, _missing)) {
    changes.add(
      RawEvidenceChange(
        kind: RawEvidenceChangeKind.removed,
        path: path,
        before: _display(left),
        after: null,
      ),
    );
  } else if (!_sameJSONScalar(left, right)) {
    changes.add(
      RawEvidenceChange(
        kind: RawEvidenceChangeKind.changed,
        path: path,
        before: _display(left),
        after: _display(right),
      ),
    );
  }
  return changes.length < _maximumChanges;
}

bool _sameJSONScalar(Object? left, Object? right) =>
    left.runtimeType == right.runtimeType && left == right;

bool _sameList(List<String> left, List<String> right) {
  if (left.length != right.length) return false;
  for (var index = 0; index < left.length; index++) {
    if (left[index] != right[index]) return false;
  }
  return true;
}

String _display(Object? value) => _truncate(jsonEncode(value));

String _truncate(String value) => value.length <= _maximumDisplayedValue
    ? value
    : '${value.substring(0, _maximumDisplayedValue)}… [truncated]';

String _pointer(String value) =>
    value.replaceAll('~', '~0').replaceAll('/', '~1');
