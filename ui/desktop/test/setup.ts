import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// jsdom does not implement scrolling. Browser coverage exercises the real
// Router restoration path; unit tests keep the API surface present so route
// commits do not turn into an unrelated render error.
Object.defineProperty(globalThis, "scrollTo", {
  configurable: true,
  value: () => undefined,
  writable: true,
});
Object.defineProperty(Element.prototype, "scrollTo", {
  configurable: true,
  value: () => undefined,
  writable: true,
});

afterEach(() => {
  cleanup();
});
