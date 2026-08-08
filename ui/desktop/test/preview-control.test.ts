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

  it("filters frozen Activity by Environment and retains the Exchange revision", async () => {
    const client = await connectPreviewControl();
    const page = await client.activities({ environmentId: "work" });
    expect(page.items).toHaveLength(1);
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
