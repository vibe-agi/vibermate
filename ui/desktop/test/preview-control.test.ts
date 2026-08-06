import { describe, expect, it } from "vitest";
import { ControlProblem } from "../src/control-client.ts";
import { connectPreviewControl } from "../src/preview-control.ts";
import { previewModeRequested } from "../src/preview-mode.ts";

describe("the browser preview host", () => {
  it("can only be selected explicitly in a development build", () => {
    expect(previewModeRequested(true, "?preview=1")).toBe(true);
    expect(previewModeRequested(true, "?preview=true")).toBe(false);
    expect(previewModeRequested(false, "?preview=1")).toBe(false);
  });

  it("drives real in-memory state transitions without a desktop session", async () => {
    const client = await connectPreviewControl();
    const initial = await client.offlineHold();
    const held = await client.enterOfflineHold(initial.revision);
    expect(held.state).toBe("held");
    expect(held.safeToDisconnect).toBe(true);
    expect(held.queuedRequests).toBeGreaterThan(0);

    const resumed = await client.resumeOfflineHold(held.revision);
    expect(resumed.state).toBe("online");
    expect(resumed.safeToDisconnect).toBe(false);

    const before = await client.approvals();
    const recognized = before.items.find(
      (approval) => approval.kind === "client_root_ask",
    );
    expect(recognized).toBeDefined();
    const deny = recognized?.choices.find(
      (choice) => choice.decision === "deny",
    );
    if (recognized === undefined || deny === undefined) {
      throw new Error("preview recognized-client approval fixture is incomplete");
    }
    const decided = await client.decideApproval(recognized, deny);
    expect(decided.state).toBe("denied");
    expect((await client.approvals()).items).toHaveLength(
      before.items.length - 1,
    );
  });

  it("paginates only canonical Exchange summaries", async () => {
    const client = await connectPreviewControl();
    const first = await client.activities();
    expect(first.items).toHaveLength(20);
    expect(first.nextCursor).toBe("cHJldmlldy1wYWdlLTI");
    for (const item of first.items) {
      expect(Object.keys(item).sort()).toEqual([
        "access",
        "id",
        "kind",
        "occurredAt",
        "parentRefs",
        "source",
        "status",
        "title",
      ]);
    }

    const second = await client.activities(first.nextCursor);
    expect(second.items).toHaveLength(20);
    expect(second.nextCursor).toBeUndefined();
    expect(second.items.some(({ status }) => status === "succeeded")).toBe(true);
    await expect(client.activities("dW5rbm93bg")).rejects.toThrow(
      "Preview Activity cursor is invalid",
    );
  });

  it("keeps a missing preview Exchange aligned with the Control API", async () => {
    const client = await connectPreviewControl();
    const missing = client.exchange("exchange-preview-missing");
    await expect(missing).rejects.toBeInstanceOf(ControlProblem);
    await expect(missing).rejects.toMatchObject({
      status: 404,
      reasonCode: "exchange_not_found",
      messageKey: "error.exchange_not_found",
    });
  });
});
