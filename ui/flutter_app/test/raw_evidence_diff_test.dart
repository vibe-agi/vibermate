import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/features/workbench/raw_evidence_diff.dart';

void main() {
  test(
    'JSON diff reports paths without exposing protected Header evidence',
    () {
      final left = _revealed(
        'client_ingress',
        {'model': 'before', 'remove': 1, 'same': true},
        headers: [
          _protected('Authorization', List.filled(64, 'a').join()),
          const RawHeaderField(
            name: 'X-Test',
            values: ['before'],
            redacted: [],
          ),
        ],
      );
      final right = _revealed(
        'provider_egress',
        {'model': 'after', 'add': 2, 'same': true},
        headers: [
          _protected('Authorization', List.filled(64, 'b').join()),
          const RawHeaderField(name: 'X-Test', values: ['after'], redacted: []),
          const RawHeaderField(
            name: 'X-New',
            values: ['visible'],
            redacted: [],
          ),
        ],
      );

      final diff = compareRawEvidence(left, right);

      expect(diff.state, RawEvidenceComparisonState.changed);
      expect(diff.mode, RawEvidenceComparisonMode.json);
      expect(
        diff.changes.map((change) => change.path),
        containsAll([
          '/headers/x-new',
          '/headers/x-test',
          '/body/add',
          '/body/model',
          '/body/remove',
        ]),
      );
      expect(
        diff.changes.map((change) => change.path),
        isNot(contains('/headers/authorization')),
      );
      final copied = diff.clipboardText();
      expect(copied, contains('/body/model'));
      expect(copied, isNot(contains(List.filled(64, 'a').join())));
      expect(copied, isNot(contains(List.filled(64, 'b').join())));
    },
  );

  test(
    'decoded compressed views compare readable JSON, not original bytes',
    () {
      final left = _revealed(
        'provider_response',
        {'ignored': true},
        contentEncoding: 'zstd',
        original: [1, 2, 3],
        decoded: {'value': 1},
      );
      final right = _revealed(
        'client_downstream',
        {'ignored': false},
        contentEncoding: 'gzip',
        original: [4, 5, 6],
        decoded: {'value': 2},
      );

      final diff = compareRawEvidence(left, right);

      expect(diff.state, RawEvidenceComparisonState.changed);
      expect(diff.leftDecoded, isTrue);
      expect(diff.rightDecoded, isTrue);
      expect(diff.changes.single.path, '/body/value');
      expect(diff.changes.single.before, '1');
      expect(diff.changes.single.after, '2');
    },
  );

  test(
    'missing, unsupported, and oversized bodies are never reported as deletion',
    () {
      final complete = _revealed('provider_egress', {'value': 1});
      final incomplete = _revealed(
        'client_ingress',
        {'value': 1},
        payloadState: 'truncated',
        digestScope: 'observed_prefix',
      );
      final unsupported = _revealed('client_ingress', {
        'value': 1,
      }, contentEncoding: 'br');
      final huge = _revealed(
        'client_ingress',
        null,
        original: List.filled(1024 * 1024 + 1, 0x61),
      );

      expect(
        compareRawEvidence(incomplete, complete).state,
        RawEvidenceComparisonState.incomplete,
      );
      expect(
        compareRawEvidence(unsupported, complete).state,
        RawEvidenceComparisonState.unsupported,
      );
      expect(
        compareRawEvidence(huge, complete).state,
        RawEvidenceComparisonState.tooLarge,
      );
      expect(compareRawEvidence(incomplete, complete).changes, isEmpty);
    },
  );
}

RevealedRawEvidence _revealed(
  String layer,
  Object? body, {
  List<RawHeaderField> headers = const [],
  String? contentEncoding,
  List<int>? original,
  Object? decoded,
  String payloadState = 'captured',
  String digestScope = 'full_body',
}) {
  final bytes = Uint8List.fromList(original ?? utf8.encode(jsonEncode(body)));
  final decodedBytes = decoded == null
      ? null
      : Uint8List.fromList(utf8.encode(jsonEncode(decoded)));
  return RevealedRawEvidence(
    envelope: RawEvidenceEnvelope(
      envelopeId: 'raw-$layer',
      layer: layer,
      scopeKind: 'managed_run',
      scopeId: 'run-test',
      exchangeId: 'exchange-test',
      observedAt: DateTime.utc(2026, 9, 26),
      expiresAt: DateTime.utc(2026, 10, 26),
      contentEncoding: contentEncoding,
      headerCount: headers.fold(
        0,
        (sum, field) => sum + field.values.length + field.redacted.length,
      ),
      trailerCount: 0,
      bodyBytes: bytes.length,
      bodySha256: List.filled(64, 'c').join(),
      digestScope: digestScope,
      payloadState: payloadState,
      payloadReason: payloadState == 'captured' ? null : 'body_limit',
      redactedCredentialFields: const ['Authorization'],
      revealAvailable: true,
    ),
    headers: headers,
    trailers: const [],
    body: bytes,
    frames: const [],
    decodedBody: decodedBytes == null
        ? null
        : RawDecodedBody(state: 'decoded', body: decodedBytes),
  );
}

RawHeaderField _protected(String name, String digest) => RawHeaderField(
  name: name,
  values: const [],
  redacted: [RawRedactedValue(digest: digest, bytes: 42)],
);
