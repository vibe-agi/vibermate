import 'control_models.dart';

/// A client protection the Runtime can name for a Capture (ADR 0026).
enum ClientProtectionId implements _Wire {
  coreOwnedStreamingFallback('core_owned_streaming_fallback'),
  structuredWorkspaceTools('structured_workspace_tools'),
  responsesDataKeepalive('responses_data_keepalive'),
  responsesWebSocketHttpFallback('responses_websocket_http_fallback');

  const ClientProtectionId(this.wire);

  @override
  final String wire;
}

/// Why the Capture's client receives a protection.
enum ClientProtectionBasis implements _Wire {
  /// The run's digest-verified release carries it.
  exactRelease('exact_release'),

  /// The client was recognized by its publisher's signature, and the
  /// recognized-client policy grants it.
  recognizedClient('recognized_client'),

  /// It applies to every client.
  operationCatalog('operation_catalog');

  const ClientProtectionBasis(this.wire);

  @override
  final String wire;
}

/// Whether a protection the client receives acts on this Capture.
enum ClientProtectionState implements _Wire {
  on('on'),

  /// The routing set gives it nothing to act on.
  inactive('inactive'),

  /// It is withheld from this run.
  off('off');

  const ClientProtectionState(this.wire);

  @override
  final String wire;
}

/// Why a protection is not on.
enum ClientProtectionReason implements _Wire {
  originalDestination('original_destination'),
  remoteWorkspace('remote_workspace'),
  operatorDisabled('operator_disabled');

  const ClientProtectionReason(this.wire);

  @override
  final String wire;
}

/// Which process applies a protection.
enum ClientProtectionApplier implements _Wire {
  /// The vibermate command sets it in the client's environment at launch.
  launcher('launcher'),

  /// The Runtime applies it to each connection or request.
  runtime('runtime');

  const ClientProtectionApplier(this.wire);

  @override
  final String wire;
}

/// The client protections that act on one Capture, as
/// `GET /api/v1/captures/{captureKey}/client-protections` answers them.
final class CaptureClientProtections {
  const CaptureClientProtections({
    required this.captureKey,
    required this.protections,
    this.client,
  });

  /// Reads one answer. A newer Runtime may name a protection, basis, state,
  /// reason or applier this build does not know: that row is left out. An
  /// answer that does not have the route's shape gives null, so the App shows
  /// no protections rather than failing the session.
  static CaptureClientProtections? tryParse(Object? json) {
    try {
      return _parse(json);
    } on ControlContractException {
      return null;
    }
  }

  static CaptureClientProtections _parse(Object? json) {
    const path = 'clientProtections';
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'captureKey', 'protections'},
      optional: const {'client'},
    );
    final captureKey = requireString(value, 'captureKey', path);
    final client = optionalString(value, 'client', path);
    final rows = requireList(value['protections'], '$path.protections');
    final protections = <CaptureClientProtection>[];
    final seen = <ClientProtectionId>{};
    for (final (index, row) in rows.indexed) {
      final protection = CaptureClientProtection._parse(
        row,
        '$path.protections[$index]',
      );
      if (protection == null) continue;
      if (!seen.add(protection.id)) {
        throw ControlContractException(
          '$path names ${protection.id.wire} twice',
        );
      }
      protections.add(protection);
    }
    return CaptureClientProtections(
      captureKey: captureKey,
      client: client,
      protections: List.unmodifiable(protections),
    );
  }

  final String captureKey;

  /// The catalogued client the run was verified or recognized as, for example
  /// `claude-code`. Null for a generic run and a manual Capture.
  final String? client;

  /// In the Runtime's order. Empty when no protection applies to the client.
  final List<CaptureClientProtection> protections;
}

/// One protection and whether it acts on the Capture.
final class CaptureClientProtection {
  const CaptureClientProtection({
    required this.id,
    required this.basis,
    required this.state,
    required this.appliedBy,
    this.reason,
  });

  /// Null when a value is one this build does not know.
  static CaptureClientProtection? _parse(Object? json, String path) {
    final value = requireObject(json, path);
    requireFields(
      value,
      path,
      required: const {'id', 'basis', 'state', 'appliedBy'},
      optional: const {'reason'},
    );
    final id = _known(
      ClientProtectionId.values,
      requireString(value, 'id', path),
    );
    final basis = _known(
      ClientProtectionBasis.values,
      requireString(value, 'basis', path),
    );
    final state = _known(
      ClientProtectionState.values,
      requireString(value, 'state', path),
    );
    final appliedBy = _known(
      ClientProtectionApplier.values,
      requireString(value, 'appliedBy', path),
    );
    final reasonWire = optionalString(value, 'reason', path);
    final reason = reasonWire == null
        ? null
        : _known(ClientProtectionReason.values, reasonWire);
    if (id == null ||
        basis == null ||
        state == null ||
        appliedBy == null ||
        (reasonWire != null && reason == null)) {
      return null;
    }
    // The Runtime gives a reason only for a protection that is not on.
    if (state == ClientProtectionState.on && reason != null) {
      throw ControlContractException('$path gives a reason for an on state');
    }
    return CaptureClientProtection(
      id: id,
      basis: basis,
      state: state,
      appliedBy: appliedBy,
      reason: reason,
    );
  }

  final ClientProtectionId id;
  final ClientProtectionBasis basis;
  final ClientProtectionState state;

  /// Present only when [state] is not on.
  final ClientProtectionReason? reason;
  final ClientProtectionApplier appliedBy;
}

abstract interface class _Wire {
  String get wire;
}

T? _known<T extends _Wire>(List<T> values, String wire) {
  for (final value in values) {
    if (value.wire == wire) return value;
  }
  return null;
}
