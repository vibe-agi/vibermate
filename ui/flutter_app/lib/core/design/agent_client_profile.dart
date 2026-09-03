enum AgentClientEvidenceFamily { session, agent, request, client }

final class AgentClientFieldSpec {
  const AgentClientFieldSpec({
    required this.labelKey,
    required this.family,
    required this.order,
  });

  final String labelKey;
  final AgentClientEvidenceFamily family;
  final int order;
}

/// Presentation metadata for one supported Agent client.
///
/// The profile deliberately maps only namespaced native fields. It gives the
/// workbench a common reading order without pretending Claude and Codex expose
/// the same wire schema.
final class AgentClientProfile {
  const AgentClientProfile._({
    required this.client,
    required this.resumeExecutable,
    required this.resumeVerb,
    required this.fields,
  });

  static const claude = AgentClientProfile._(
    client: 'claude',
    resumeExecutable: 'claude',
    resumeVerb: '--resume',
    fields: <String, AgentClientFieldSpec>{
      'claude.session_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.session_id',
        family: AgentClientEvidenceFamily.session,
        order: 10,
      ),
      'claude.legacy_session_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.legacy_session_id',
        family: AgentClientEvidenceFamily.session,
        order: 20,
      ),
      'claude.agent_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.agent_id',
        family: AgentClientEvidenceFamily.agent,
        order: 10,
      ),
      'claude.parent_agent_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.parent_agent_id',
        family: AgentClientEvidenceFamily.agent,
        order: 20,
      ),
      'claude.tool_use_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.tool_use_id',
        family: AgentClientEvidenceFamily.agent,
        order: 30,
      ),
      'claude.spawned_agent_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.spawned_agent_id',
        family: AgentClientEvidenceFamily.agent,
        order: 35,
      ),
      'claude.description': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.description',
        family: AgentClientEvidenceFamily.agent,
        order: 40,
      ),
      'claude.agent_type': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.agent_type',
        family: AgentClientEvidenceFamily.agent,
        order: 50,
      ),
      'claude.skill': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.skill',
        family: AgentClientEvidenceFamily.agent,
        order: 60,
      ),
      'claude.spawn_depth': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.spawn_depth',
        family: AgentClientEvidenceFamily.agent,
        order: 70,
      ),
      'claude.request_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.request_id',
        family: AgentClientEvidenceFamily.request,
        order: 10,
      ),
      'claude.prompt_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.prompt_id',
        family: AgentClientEvidenceFamily.request,
        order: 15,
      ),
      'claude.event_uuid': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.event_uuid',
        family: AgentClientEvidenceFamily.request,
        order: 20,
      ),
      'claude.parent_event_uuid': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.parent_event_uuid',
        family: AgentClientEvidenceFamily.request,
        order: 30,
      ),
      'claude.source_tool_assistant_uuid': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.source_tool_assistant_uuid',
        family: AgentClientEvidenceFamily.request,
        order: 40,
      ),
      'claude.source_provider_message_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.source_provider_message_id',
        family: AgentClientEvidenceFamily.request,
        order: 50,
      ),
      'claude.content_block_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.content_block_id',
        family: AgentClientEvidenceFamily.request,
        order: 60,
      ),
    },
  );

  static const codex = AgentClientProfile._(
    client: 'codex',
    resumeExecutable: 'codex',
    resumeVerb: 'resume',
    fields: <String, AgentClientFieldSpec>{
      'codex.session_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.session_id',
        family: AgentClientEvidenceFamily.session,
        order: 10,
      ),
      'codex.context_window_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.context_window_id',
        family: AgentClientEvidenceFamily.session,
        order: 20,
      ),
      'codex.compaction_window_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.compaction_window_id',
        family: AgentClientEvidenceFamily.session,
        order: 30,
      ),
      'codex.previous_window_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.previous_window_id',
        family: AgentClientEvidenceFamily.session,
        order: 40,
      ),
      'codex.first_window_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.first_window_id',
        family: AgentClientEvidenceFamily.session,
        order: 50,
      ),
      'codex.thread_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.thread_id',
        family: AgentClientEvidenceFamily.agent,
        order: 10,
      ),
      'codex.parent_thread_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.parent_thread_id',
        family: AgentClientEvidenceFamily.agent,
        order: 20,
      ),
      'codex.forked_from_thread_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.forked_from_thread_id',
        family: AgentClientEvidenceFamily.agent,
        order: 30,
      ),
      'codex.agent_nickname': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.agent_nickname',
        family: AgentClientEvidenceFamily.agent,
        order: 40,
      ),
      'codex.agent_path': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.agent_path',
        family: AgentClientEvidenceFamily.agent,
        order: 50,
      ),
      'codex.agent_role': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.agent_role',
        family: AgentClientEvidenceFamily.agent,
        order: 60,
      ),
      'codex.spawn_depth': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.spawn_depth',
        family: AgentClientEvidenceFamily.agent,
        order: 70,
      ),
      'codex.thread_source': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.thread_source',
        family: AgentClientEvidenceFamily.agent,
        order: 80,
      ),
      'codex.turn_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.turn_id',
        family: AgentClientEvidenceFamily.request,
        order: 10,
      ),
      'openai_responses.previous_response_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.previous_response_id',
        family: AgentClientEvidenceFamily.request,
        order: 20,
      ),
      'codex.response_item_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.response_item_id',
        family: AgentClientEvidenceFamily.request,
        order: 30,
      ),
      'codex.reasoning_item_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.reasoning_item_id',
        family: AgentClientEvidenceFamily.request,
        order: 40,
      ),
      'codex.call_id': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.call_id',
        family: AgentClientEvidenceFamily.request,
        order: 50,
      ),
      'codex.compaction_hash': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.compaction_hash',
        family: AgentClientEvidenceFamily.request,
        order: 60,
      ),
      'codex.cli_version': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.cli_version',
        family: AgentClientEvidenceFamily.client,
        order: 10,
      ),
      'codex.model_provider': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.model_provider',
        family: AgentClientEvidenceFamily.client,
        order: 20,
      ),
      'codex.originator': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.originator',
        family: AgentClientEvidenceFamily.client,
        order: 30,
      ),
      'codex.source': AgentClientFieldSpec(
        labelKey: 'exchange.client.field.local_source',
        family: AgentClientEvidenceFamily.client,
        order: 40,
      ),
    },
  );

  static AgentClientProfile resolve(String client) => switch (client) {
    'claude' => claude,
    'codex' => codex,
    _ => AgentClientProfile._(
      client: client,
      resumeExecutable: client,
      resumeVerb: 'resume',
      fields: const <String, AgentClientFieldSpec>{},
    ),
  };

  final String client;
  final String resumeExecutable;
  final String resumeVerb;
  final Map<String, AgentClientFieldSpec> fields;

  String resumeCommand(String sessionId) =>
      '$resumeExecutable $resumeVerb ${_shellQuote(sessionId)}';

  AgentClientFieldSpec field(String nativeName) =>
      fields[nativeName] ??
      const AgentClientFieldSpec(
        labelKey: '',
        family: AgentClientEvidenceFamily.client,
        order: 1000,
      );
}

String _shellQuote(String value) => "'${value.replaceAll("'", "'\\''")}'";
