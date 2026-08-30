import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/bootstrap/desktop_runtime.dart';
import 'package:vibermate_app/core/bootstrap/terminal_command.dart';
import 'package:vibermate_app/features/workbench/environment_editing.dart';

void main() {
  final daemonPath = Platform.environment['VIBERMATE_LIVE_TEST_DAEMON'];
  final commandPath = Platform.environment['VIBERMATE_LIVE_TEST_COMMAND'];

  test(
    'Flutter runtime launches the real daemon and reads Control API authority',
    () async {
      final root = await Directory.systemTemp.createTemp(
        'vibermate-flutter-live.',
      );
      final runSuffix = DateTime.now().microsecondsSinceEpoch.toRadixString(36);
      DesktopRuntime? runtime;
      try {
        runtime = await DesktopRuntime.start(
          daemonPath: daemonPath,
          cacheDirectory: '${root.path}/cache',
          dataDirectory: '${root.path}/data',
          remoteServerListenAddress: '127.0.0.1:0',
        );
        final dashboard = await runtime.api.loadDashboard();
        expect(dashboard.status.ready, isTrue);
        expect(dashboard.status.healthy, isTrue);
        expect(dashboard.status.instanceId, isNotEmpty);
        expect(dashboard.environments, isNotEmpty);
        final network = await runtime.api.loadNetwork();
        expect(network.rules.revision, greaterThan(0));
        expect(const {
          'monitor',
          'ask_unknown',
          'deny_unknown',
        }, contains(network.rules.mode));
        expect(await runtime.api.pendingApprovals(), isEmpty);

        final initialOffline = dashboard.status.offlineHold;
        expect(initialOffline.state, 'online');
        expect(initialOffline.safeToDisconnect, isFalse);
        final held = await runtime.api.enterOfflineHold(initialOffline);
        expect(held.state, 'held');
        expect(held.revision, greaterThan(initialOffline.revision));
        expect(held.safeToDisconnect, isTrue);
        final heldDashboard = await runtime.api.loadDashboard();
        expect(heldDashboard.status.offlineHold.revision, held.revision);
        expect(heldDashboard.status.offlineHold.safeToDisconnect, isTrue);
        final resumed = await runtime.api.resumeOfflineHold(held);
        expect(resumed.state, 'online');
        expect(resumed.revision, greaterThan(held.revision));
        expect(resumed.safeToDisconnect, isFalse);

        final environmentId = 'flutter-live-observe-$runSuffix';
        final environmentDraft = await runtime.api.saveEnvironmentDraft(
          environmentId: environmentId,
          expectedBaseRevision: 0,
          input: const EnvironmentDraftInput(
            expectedDraftRevision: 0,
            name: 'Flutter live Environment',
            state: 'active',
            clientEndpoints: [],
            pluginBindings: [],
            budgetPolicy: EnvironmentBudgetPolicy(id: '', revision: 0),
            contentRecording: EnvironmentContentRecordingPolicy(
              mode: 'metadata_only',
              retentionDays: 7,
            ),
            launchEnvironment: EnvironmentLaunchPolicy.empty(),
            policySet: EnvironmentPolicySet(toolMode: 'observe'),
          ),
        );
        expect(environmentDraft.baseRevision, 0);
        expect(environmentDraft.draftRevision, 1);
        final environmentImpact = await runtime.api.previewEnvironmentDraft(
          environmentId,
          environmentDraft.draftRevision,
        );
        expect(environmentImpact.continuingCaptures, isEmpty);
        final environmentPublish = await runtime.api.publishEnvironmentDraft(
          environmentId,
          environmentDraft.draftRevision,
        );
        expect(environmentPublish.environment.revision, 1);
        expect(environmentPublish.environment.clientEndpoints, isEmpty);
        expect(
          environmentPublish.environment.contentRecording.mode,
          'metadata_only',
        );
        final publishedDashboard = await runtime.api.loadDashboard();
        expect(
          publishedDashboard.environments
              .firstWhere((value) => value.id == environmentId)
              .revision,
          1,
        );

        final endpoint = await runtime.api.createUpstreamEndpoint(
          id: 'target.flutter.live-$runSuffix',
          displayName: 'Flutter live relay',
          origin: 'https://flutter-live.example',
          backendProtocols: const [
            'anthropic_messages',
            'openai_responses',
            'openai_chat',
          ],
        );
        expect(endpoint.accountKinds, contains('anthropic_api_key'));
        expect(endpoint.backendProtocols, [
          'anthropic_messages',
          'openai_responses',
          'openai_chat',
        ]);
        final account = await runtime.api.createProviderAccount(
          id: 'account.flutter.live-$runSuffix',
          displayName: 'Flutter live Account',
          upstreamEndpointId: endpoint.id,
          kind: 'anthropic_api_key',
          secret: 'flutter-live-secret-one',
          headerPolicy: const ProviderAccountHeaderPolicy(),
        );
        expect(account.upstreamEndpointId, endpoint.id);
        expect(account.credentialEpoch, 1);
        final rotated = await runtime.api.replaceProviderAccountCredential(
          account: account,
          secret: 'flutter-live-secret-two',
          headerPolicy: const ProviderAccountHeaderPolicy(),
        );
        expect(rotated.credentialEpoch, 2);
        final deleted = await runtime.api.deleteProviderAccount(rotated);
        expect(deleted.deleted, isTrue);
        expect(deleted.references, isEmpty);

        final routeAccount = await runtime.api.createProviderAccount(
          id: 'account.flutter.route-$runSuffix',
          displayName: 'Flutter Route Account',
          upstreamEndpointId: endpoint.id,
          kind: 'anthropic_api_key',
          secret: 'flutter-live-route-secret',
          headerPolicy: const ProviderAccountHeaderPolicy(),
        );
        final anthropicRoutedEndpoints = appendEnvironmentUpstreamEndpoint(
          endpoints: const [],
          upstreamEndpoint: endpoint,
          accountPolicy: fixedRouteAccountPolicy(routeAccount),
          availableAccounts: [routeAccount],
          clientProtocol: 'anthropic_messages',
          identityNonce: '$runSuffix-anthropic',
        );
        final routedEndpoints = appendEnvironmentUpstreamEndpoint(
          endpoints: anthropicRoutedEndpoints,
          upstreamEndpoint: endpoint,
          accountPolicy: fixedRouteAccountPolicy(routeAccount),
          availableAccounts: [routeAccount],
          clientProtocol: 'openai_responses',
          identityNonce: '$runSuffix-responses',
        );
        final routedEnvironmentId = 'flutter-live-routed-$runSuffix';
        final routedDraft = await runtime.api.saveEnvironmentDraft(
          environmentId: routedEnvironmentId,
          expectedBaseRevision: 0,
          input: EnvironmentDraftInput(
            expectedDraftRevision: 0,
            name: 'Flutter routed Environment',
            state: 'active',
            clientEndpoints: routedEndpoints,
            pluginBindings: const [],
            budgetPolicy: const EnvironmentBudgetPolicy(id: '', revision: 0),
            contentRecording: const EnvironmentContentRecordingPolicy(
              mode: 'full',
              retentionDays: 30,
            ),
            launchEnvironment: const EnvironmentLaunchPolicy.empty(),
            policySet: const EnvironmentPolicySet(toolMode: 'observe'),
          ),
        );
        final routedImpact = await runtime.api.previewEnvironmentDraft(
          routedEnvironmentId,
          routedDraft.draftRevision,
        );
        expect(routedImpact.continuingCaptures, isEmpty);
        final routedPublish = await runtime.api.publishEnvironmentDraft(
          routedEnvironmentId,
          routedDraft.draftRevision,
        );
        expect(routedPublish.environment.routes, hasLength(2));
        for (final routed in routedPublish.environment.routes) {
          expect(routed.endpointId, endpoint.id);
          expect(routed.accountPolicy.fixedAccountId, routeAccount.id);
          expect(routed.accountPolicy.accounts.single.id, routeAccount.id);
          expect(
            routed.accountPolicy.accounts.single.revision,
            routeAccount.revision,
          );
        }
        final blockedDelete = await runtime.api.deleteProviderAccount(
          routeAccount,
        );
        expect(blockedDelete.deleted, isFalse);
        expect(blockedDelete.references, hasLength(2));
        expect(
          blockedDelete.references.map((reference) => reference.environmentId),
          everyElement(routedEnvironmentId),
        );
        final cleanupDraft = await runtime.api.saveEnvironmentDraft(
          environmentId: routedEnvironmentId,
          expectedBaseRevision: routedPublish.environment.revision,
          input: const EnvironmentDraftInput(
            expectedDraftRevision: 0,
            name: 'Flutter routed Environment',
            state: 'active',
            clientEndpoints: [],
            pluginBindings: [],
            budgetPolicy: EnvironmentBudgetPolicy(id: '', revision: 0),
            contentRecording: EnvironmentContentRecordingPolicy(
              mode: 'full',
              retentionDays: 30,
            ),
            launchEnvironment: EnvironmentLaunchPolicy.empty(),
            policySet: EnvironmentPolicySet(toolMode: 'observe'),
          ),
        );
        expect(
          cleanupDraft.draftRevision,
          greaterThan(routedDraft.draftRevision),
        );
        await runtime.api.previewEnvironmentDraft(
          routedEnvironmentId,
          cleanupDraft.draftRevision,
        );
        final cleanupPublish = await runtime.api.publishEnvironmentDraft(
          routedEnvironmentId,
          cleanupDraft.draftRevision,
        );
        expect(cleanupPublish.environment.clientEndpoints, isEmpty);

        final readdedEndpoints = appendEnvironmentUpstreamEndpoint(
          endpoints: cleanupPublish.environment.clientEndpoints,
          upstreamEndpoint: endpoint,
          accountPolicy: fixedRouteAccountPolicy(routeAccount),
          availableAccounts: [routeAccount],
          identityNonce: 'readded-$runSuffix',
        );
        final readdedDraft = await runtime.api.saveEnvironmentDraft(
          environmentId: routedEnvironmentId,
          expectedBaseRevision: cleanupPublish.environment.revision,
          input: EnvironmentDraftInput.fromEnvironment(
            cleanupPublish.environment,
            expectedDraftRevision: 0,
            clientEndpoints: readdedEndpoints,
          ),
        );
        await runtime.api.previewEnvironmentDraft(
          routedEnvironmentId,
          readdedDraft.draftRevision,
        );
        final readdedPublish = await runtime.api.publishEnvironmentDraft(
          routedEnvironmentId,
          readdedDraft.draftRevision,
        );
        expect(
          readdedPublish.environment.routes.single.endpointId,
          endpoint.id,
        );
        final originalAnthropicEndpoint = routedPublish
            .environment
            .clientEndpoints
            .firstWhere(
              (client) => client.protocolPlans.any(
                (plan) => plan.clientProtocol == 'anthropic_messages',
              ),
            );
        expect(
          readdedPublish.environment.clientEndpoints.single.id,
          isNot(originalAnthropicEndpoint.id),
        );

        final finalCleanupDraft = await runtime.api.saveEnvironmentDraft(
          environmentId: routedEnvironmentId,
          expectedBaseRevision: readdedPublish.environment.revision,
          input: EnvironmentDraftInput.fromEnvironment(
            readdedPublish.environment,
            expectedDraftRevision: 0,
            clientEndpoints: const [],
          ),
        );
        await runtime.api.previewEnvironmentDraft(
          routedEnvironmentId,
          finalCleanupDraft.draftRevision,
        );
        await runtime.api.publishEnvironmentDraft(
          routedEnvironmentId,
          finalCleanupDraft.draftRevision,
        );
        final cleanedDelete = await runtime.api.deleteProviderAccount(
          routeAccount,
        );
        expect(cleanedDelete.deleted, isTrue);

        final manualContext = await runtime.api.manualCaptureContext(
          'system_transparent',
        );
        expect(manualContext.environmentId, 'system_transparent');
        expect(manualContext.root, isNotNull);
        expect(
          manualContext.protectedAuthorities,
          orderedEquals([
            'api.anthropic.com:443',
            'api.openai.com:443',
            'chatgpt.com:443',
          ]),
        );
        expect(manualContext.managedCredentialAuthorities, isEmpty);
        final createdManual = await runtime.api.createManualCapture(
          context: manualContext,
          displayName: 'Flutter live client',
          clientClass: 'desktop_app',
          lifetime: 'until_revoked',
        );
        expect(createdManual.grant.capture.state, 'active');
        expect(createdManual.grant.proxyPassword, isNotEmpty);
        expect(createdManual.stateTag, isNotEmpty);
        final manualActivities = await runtime.api.activities(
          manualCaptureId: createdManual.grant.capture.id,
        );
        expect(manualActivities.items, isEmpty);
        expect(manualActivities.nextCursor, isNull);

        final currentManual = await runtime.api.manualCaptureState(
          createdManual.grant.capture.id,
        );
        expect(currentManual.stateTag, createdManual.stateTag);
        final rotatedManual = await runtime.api.rotateManualCapture(
          currentManual,
        );
        expect(
          rotatedManual.grant.proxyPassword,
          isNot(createdManual.grant.proxyPassword),
        );
        expect(rotatedManual.stateTag, isNot(createdManual.stateTag));

        await runtime.api.revokeManualCapture(
          manualCaptureId: rotatedManual.grant.capture.id,
          stateTag: rotatedManual.stateTag,
        );
        final revokedManual = await runtime.api.manualCaptureState(
          rotatedManual.grant.capture.id,
        );
        expect(revokedManual.state, 'revoked');
        expect(revokedManual.stateTag, isNot(rotatedManual.stateTag));
      } finally {
        await runtime?.close();
        if (await root.exists()) await root.delete(recursive: true);
      }
    },
    skip: daemonPath == null
        ? 'Set VIBERMATE_LIVE_TEST_DAEMON to an absolute vibermated path.'
        : false,
    timeout: const Timeout(Duration(minutes: 2)),
  );

  test(
    'Flutter inspects the exact packaged terminal command without a shell',
    () async {
      final canonicalCommand = await File(commandPath!).resolveSymbolicLinks();
      final commandMetadata = await File(canonicalCommand).stat();
      expect(
        commandMetadata.type,
        FileSystemEntityType.file,
        reason: 'The packaged terminal command must be a regular file.',
      );
      expect(
        commandMetadata.mode & 0x49,
        isNot(0),
        reason:
            'The packaged terminal command must have at least one executable bit.',
      );
      expect(
        canonicalCommand,
        endsWith('${Platform.pathSeparator}vibermate'),
        reason: 'The packaged terminal command identity is path-bound.',
      );
      final service = PackagedTerminalCommandService(commandPath: commandPath);
      final status = await service.inspect();
      expect(status.sourcePath, canonicalCommand);
      expect(status.targetPath, endsWith('/.local/bin/vibermate'));
      expect(TerminalCommandState.values, contains(status.state));
    },
    skip: commandPath == null
        ? 'Set VIBERMATE_LIVE_TEST_COMMAND to an absolute packaged vibermate path.'
        : false,
    timeout: const Timeout(Duration(seconds: 30)),
  );

  test(
    'packaged CLI discovers the Flutter-owned daemon through one application identity',
    () async {
      final home = await Directory.systemTemp.createTemp(
        'vibermate-flutter-discovery.',
      );
      DesktopRuntime? runtime;
      try {
        runtime = await DesktopRuntime.start(
          daemonPath: daemonPath,
          homeDirectory: home.path,
          remoteServerListenAddress: '127.0.0.1:0',
        );
        final dashboard = await runtime.api.loadDashboard();
        expect(
          dashboard.environments.any(
            (environment) => environment.id == 'system_transparent',
          ),
          isTrue,
        );
        final result = await Process.run(
          commandPath!,
          const ['run', '--env', 'system_transparent', '--', '/usr/bin/true'],
          environment: {'HOME': home.path},
          includeParentEnvironment: true,
          runInShell: false,
        ).timeout(const Duration(seconds: 15));
        expect(
          result.exitCode,
          0,
          reason:
              'The packaged CLI must resolve the Flutter daemon discovery record: '
              '${result.stderr}',
        );
        final captures = (await runtime.api.loadDashboard()).captures;
        expect(
          captures.any((capture) => capture.managedRun != null),
          isTrue,
          reason: 'The CLI run must be observable as real Capture evidence.',
        );
      } finally {
        await runtime?.close();
        if (await home.exists()) await home.delete(recursive: true);
      }
    },
    skip: daemonPath == null || commandPath == null
        ? 'Set both packaged daemon and CLI live-test paths.'
        : false,
    timeout: const Timeout(Duration(seconds: 30)),
  );

  test(
    'an unexpected packaged daemon exit is observable before intentional close',
    () async {
      final root = await Directory.systemTemp.createTemp(
        'vibermate-flutter-exit.',
      );
      DesktopRuntime? runtime;
      try {
        runtime = await DesktopRuntime.start(
          daemonPath: daemonPath,
          cacheDirectory: '${root.path}/cache',
          dataDirectory: '${root.path}/data',
          remoteServerListenAddress: '127.0.0.1:0',
        );
        expect(runtime.isClosed, isFalse);
        expect(
          Process.killPid(runtime.daemonPid, ProcessSignal.sigterm),
          isTrue,
        );
        await runtime.exitCode.timeout(const Duration(seconds: 5));
        expect(runtime.isClosed, isFalse);
        await runtime.close();
        expect(runtime.isClosed, isTrue);
      } finally {
        await runtime?.close();
        if (await root.exists()) await root.delete(recursive: true);
      }
    },
    skip: daemonPath == null
        ? 'Set VIBERMATE_LIVE_TEST_DAEMON to an absolute vibermated path.'
        : false,
    timeout: const Timeout(Duration(seconds: 30)),
  );
}
