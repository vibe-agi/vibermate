import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { describe, expect, it } from "vitest";
import {
  DashboardRouterProvider,
  createDashboardRouter,
} from "../src/app-router.tsx";
import { DashboardQueryRuntime } from "../src/dashboard-runtime.ts";
import { createI18n } from "../src/i18n.ts";
import { connectPreviewControl } from "../src/preview-control.ts";

async function renderDashboard(initialEntry = "/captures") {
  const client = await connectPreviewControl();
  const model = new DashboardQueryRuntime(client, 60_000);
  const i18n = await createI18n("en-US");
  function Dashboard() {
    const [router] = useState(() => createDashboardRouter(
      createMemoryHistory({ initialEntries: [initialEntry] }),
      { model, preview: true },
    ));
    return (
      <I18nextProvider i18n={i18n}>
        <DashboardRouterProvider model={model} preview router={router} />
      </I18nextProvider>
    );
  }
  const rendered = render(<Dashboard />);
  return { ...rendered, model };
}

describe("Environment-first Desktop workspace", () => {
  it("starts with captures and exposes the frozen runtime assignment", async () => {
    const { model } = await renderDashboard();
    expect(await screen.findByRole("heading", { name: "Captures" })).toBeTruthy();
    expect(await screen.findByText("claude")).toBeTruthy();
    expect(screen.getByRole("link", { name: /claude/u }).getAttribute("href"))
      .toContain("captures/managed_run%3Arun-preview");
    await model.dispose();
  });

  it("navigates to Environment ownership without an Access compatibility page", async () => {
    const { model } = await renderDashboard();
    fireEvent.click(await screen.findByRole("link", { name: "Environments" }));
    expect(await screen.findByRole("heading", { name: "Environments" })).toBeTruthy();
    expect(screen.getByText("Transparent")).toBeTruthy();
    expect(screen.queryByText("AI Access")).toBeNull();
    await model.dispose();
  });

  it("switches the visible policy mode through the typed control client", async () => {
    const { model } = await renderDashboard("/policies/approvals");
    expect(await screen.findByRole("heading", { name: "Policy & approvals" })).toBeTruthy();
    const open = screen.getByRole("radio", { name: "Open" });
    await waitFor(() => expect((open as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(open);
    await waitFor(() => expect(open.getAttribute("aria-checked")).toBe("true"));
    await model.dispose();
  });

  it("renders request evidence from frozen Environment and Route references", async () => {
    const { model } = await renderDashboard("/captures/requests");
    expect(await screen.findByRole("heading", { name: "Requests" })).toBeTruthy();
    expect(await screen.findByText("work")).toBeTruthy();
    expect(await screen.findByText("claude-official")).toBeTruthy();
    await model.dispose();
  });
});
