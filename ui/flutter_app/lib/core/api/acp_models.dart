import 'control_models.dart';

final class ACPRecord {
  const ACPRecord({
    required this.runId,
    required this.mode,
    required this.expired,
    required this.agentName,
    required this.agentVersion,
    required this.protocolVersion,
    required this.sessions,
    required this.prompts,
    required this.incomplete,
    required this.finished,
    this.exitCode,
    this.needsAuthentication = false,
  });

  factory ACPRecord.fromJson(Object? value) {
    final json = requireObject(value, 'acpObservation');
    final snapshot = json['snapshot'] as Map<String, dynamic>;
    final agent = snapshot['agent'] as Map<String, dynamic>;
    return ACPRecord(
      runId: json['runId'] as String,
      mode: (json['policy'] as Map<String, dynamic>)['mode'] as String,
      expired: json['expired'] as bool,
      agentName: agent['name'] as String,
      agentVersion: agent['version'] as String,
      protocolVersion: snapshot['protocolVersion'] as int,
      sessions: [
        for (final value in snapshot['sessions'] as List? ?? [])
          ACPSession.fromJson(value as Map<String, dynamic>),
      ],
      prompts: [
        for (final value in snapshot['prompts'] as List? ?? [])
          ACPPrompt.fromJson(value as Map<String, dynamic>),
      ],
      incomplete: snapshot['incomplete'] as bool,
      finished: snapshot['final'] as bool,
      exitCode: snapshot['exitCode'] as int?,
      needsAuthentication: snapshot['needsAuthentication'] as bool? ?? false,
    );
  }

  final String runId, mode, agentName, agentVersion;
  final int protocolVersion;
  final List<ACPSession> sessions;
  final List<ACPPrompt> prompts;
  final bool expired, incomplete, finished;
  final bool needsAuthentication;
  final int? exitCode;
}

final class ACPSession {
  const ACPSession({
    required this.id,
    required this.cwd,
    required this.operation,
  });
  factory ACPSession.fromJson(Map<String, dynamic> json) => ACPSession(
    id: json['id'] as String,
    cwd: json['cwd'] as String,
    operation: json['operation'] as String,
  );
  final String id, cwd, operation;
}

final class ACPPrompt {
  const ACPPrompt({
    required this.sequence,
    required this.sessionId,
    required this.state,
    required this.stopReason,
    required this.userText,
    required this.agentText,
    required this.toolCalls,
  });
  factory ACPPrompt.fromJson(Map<String, dynamic> json) => ACPPrompt(
    sequence: json['sequence'] as int,
    sessionId: json['sessionId'] as String,
    state: json['state'] as String,
    stopReason: json['stopReason'] as String? ?? '',
    userText: json['userText'] as String? ?? '',
    agentText: json['agentText'] as String? ?? '',
    toolCalls: json['toolCalls'] as int,
  );
  final int sequence, toolCalls;
  final String sessionId, state, stopReason, userText, agentText;
}
