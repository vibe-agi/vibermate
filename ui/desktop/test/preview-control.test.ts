import { describe, expect, it } from "vitest";
import { ControlProblem } from "../src/control-client.ts";
import { connectPreviewControl } from "../src/preview-control.ts";
import { previewModeRequested } from "../src/preview-mode.ts";

describe("the Environment-first browser preview host", () => {
  it("can only be selected explicitly in a development build", () => {
    expect(previewModeRequested(true, "?preview=1")).toBe(true);
    expect(previewModeRequested(true, "?preview=true")).toBe(false);
    expect(previewModeRequested(false, "?preview=1")).toBe(false);
  });

  it("publishes an Environment draft and preserves the historical revision", async () => {
    const client = await connectPreviewControl();
    const current = await client.environment("work");
    await expect(client.environmentDraft("work")).rejects.toMatchObject({
      reasonCode: "environment_draft_not_found",
    });
    const saved = await client.saveEnvironmentDraft(
      "work",
      current.revision,
      {
        expectedDraftRevision: 0,
        name: "Updated work",
        state: current.state,
        clientEndpoints: current.clientEndpoints,
        pluginBindings: current.pluginBindings,
        budgetPolicy: current.budgetPolicy,
        egressPolicy: current.egressPolicy,
        contentRecording: current.contentRecording,
        policySet: current.policySet,
      },
    );
    await expect(client.environmentDraft("work")).resolves.toEqual(saved);
    const impact = await client.previewEnvironmentDraft(
      "work",
      saved.draftRevision,
    );
    expect(impact.hotSwitchCount).toBe(1);
    expect(impact.restartRequiredCount).toBe(0);

    const published = await client.publishEnvironmentDraft(
      "work",
      saved.draftRevision,
    );
    expect(published.environment.name).toBe("Updated work");
    expect(published.environment.revision).toBe(current.revision + 1);
    await expect(
      client.environmentRevision("work", current.revision),
    ).resolves.toEqual(current);
  });

  it("creates and rotates a managed ProviderAccount without exposing its secret", async () => {
    const client = await connectPreviewControl();
    const created = await client.createProviderAccount({
      id: "openai-personal",
      displayName: "OpenAI Personal",
      kind: "openai_api_key",
      secret: "private-preview-secret",
    });
    expect(created).toMatchObject({
      id: "openai-personal",
      realmId: "openai.platform",
      credentialState: "ready",
      credentialEpoch: 1,
    });
    expect(JSON.stringify(created)).not.toContain("private-preview-secret");
    const rotated = await client.replaceProviderAccountCredential(
      created.id,
      created.credentialEpoch,
      { secret: "second-private-preview-secret" },
    );
    expect(rotated.credentialEpoch).toBe(2);

    const oauth = await client.createProviderAccount({
      id: "claude-oauth",
      displayName: "Claude OAuth",
      kind: "claude_oauth_token",
      secret: "oauth-preview-secret",
    });
    expect(oauth).toMatchObject({
      kind: "claude_oauth_token",
      realmId: "anthropic.official",
      credentialState: "ready",
    });
    expect(JSON.stringify(oauth)).not.toContain("oauth-preview-secret");

    await expect(
      client.deleteProviderAccount(oauth.id, oauth.credentialEpoch),
    ).resolves.toEqual({ deleted: true, referenceCount: 0, references: [] });
    await expect(client.providerAccount(oauth.id)).rejects.toMatchObject({
      reasonCode: "not_found",
    });
  });

  it("keeps equal raw IDs distinct across Capture kinds and switches one assignment", async () => {
    const client = await connectPreviewControl();
    const created = await client.createManualCapture({
      environmentId: "work",
      displayName: "Manual preview",
      clientClass: "cli",
      lifetime: "until_revoked",
      confirmationToken: `ctx_${"A".repeat(43)}`,
    });
    const manualKey = `manual_capture:${created.grant.capture.id}`;
    const before = await client.captureAssignment(manualKey);
    const switched = await client.switchCaptureEnvironment(
      manualKey,
      before.revision,
      "system_transparent",
    );

    expect(switched.assignment.captureKey).toBe(manualKey);
    expect(switched.assignment.environmentId).toBe("system_transparent");
    expect((await client.capture("managed_run:run-preview")).kind).toBe(
      "managed_run",
    );
    expect((await client.capture(manualKey)).kind).toBe("manual_capture");
  });

  it("keeps the future workspace default separate from the active Capture assignment", async () => {
    const client = await connectPreviewControl();
    const captureKey = "managed_run:run-preview";
    const assignmentBefore = await client.captureAssignment(captureKey);

    const initial = await client.workspaceEnvironmentDefault(
      "machine-preview",
      "workspace-preview",
    );
    expect(initial).toBeDefined();

    const selected = await client.setWorkspaceEnvironmentDefault(
      "machine-preview",
      "workspace-preview",
      initial?.revision ?? 0,
      "work",
    );
    expect(selected.environmentId).toBe("work");
    await expect(client.captureAssignment(captureKey)).resolves.toEqual(
      assignmentBefore,
    );

    await client.clearWorkspaceEnvironmentDefault(
      selected.machineId,
      selected.workspaceId,
      selected.revision,
    );
    await expect(
      client.workspaceEnvironmentDefault(selected.machineId, selected.workspaceId),
    ).resolves.toBeUndefined();
    await client.setWorkspaceEnvironmentDefault(
      selected.machineId,
      selected.workspaceId,
      0,
      "work",
    );
  });

  it("filters frozen Activity by Environment and retains the Exchange revision", async () => {
    const client = await connectPreviewControl();
    const page = await client.activities({ environmentId: "work" });
    expect(page.items).toHaveLength(3);
    expect(page.items.map((item) => item.parentRefs.captureRunId)).toEqual([
      "run-preview",
      "run-preview",
      "run-unsupported",
    ]);
    expect(page.items[0]?.environment).toMatchObject({
      id: "work",
      revision: 3,
      routeId: "claude-official",
      routeRevision: 2,
    });
    const detail = await client.exchange("exchange-preview");
    expect(detail.environment).toEqual(page.items[0]?.environment);
    await expect(client.exchange("missing")).rejects.toBeInstanceOf(
      ControlProblem,
    );
  });

  it("drives Offline Hold and Approval transitions without a desktop session", async () => {
    const client = await connectPreviewControl();
    const online = await client.offlineHold();
    const held = await client.enterOfflineHold(online.revision);
    expect(held.state).toBe("held");
    const resumed = await client.resumeOfflineHold(held.revision);
    expect(resumed.state).toBe("online");

    const pending = (await client.approvals()).items[0];
    if (pending === undefined) throw new Error("preview approval missing");
    const deny = pending.choices.find((choice) => choice.decision === "deny");
    if (deny === undefined) throw new Error("preview deny choice missing");
    expect((await client.decideApproval(pending, deny)).state).toBe("denied");
    expect((await client.approvals()).items).toHaveLength(0);
  });
});
