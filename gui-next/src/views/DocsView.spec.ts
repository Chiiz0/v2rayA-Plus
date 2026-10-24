// @vitest-environment happy-dom
import { beforeEach, describe, expect, test, vi } from "vitest";
import { flushPromises } from "@vue/test-utils";
import { mountWithApp } from "@/test/mount";
import { useAppStore } from "@/stores/app";
import DocsView from "./DocsView.vue";

const api = vi.hoisted(() => ({ getParams: vi.fn() }));
vi.mock("@/api", () => api);

// the Markdown is a dynamic import; wait until the article is in
const settle = (w: { find: (s: string) => { exists(): boolean } }) =>
  vi.waitFor(async () => {
    await flushPromises();
    expect(w.find(".docs__body h1").exists()).toBe(true);
  });

describe("the documentation page", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  test("renders the Markdown of the first section with heading ids", async () => {
    const w = mountWithApp(DocsView);
    await settle(w);
    expect(w.find(".docs__body h1").text()).toBe("Quick start");
    expect(w.find(".docs__list .v-list-item--active").text()).toBe(
      "Quick start",
    );
    expect(w.find(".docs__body").text()).toContain(
      "Both interfaces share server-side configuration",
    );
    expect(api.getParams).not.toHaveBeenCalled();
    w.unmount();
  });

  test("documents installed startup options without calling the unsupported params API", async () => {
    const w = mountWithApp(DocsView);
    await settle(w);
    await w
      .findAll(".docs__list .v-list-item")
      .find((item) => item.text() === "Flags and environment")!
      .trigger("click");
    await vi.waitFor(async () => {
      await flushPromises();
      expect(w.find(".docs__body h1").text()).toBe(
        "Startup parameters and database",
      );
      expect(w.find(".docs__body").text()).toContain("V2RAYA_ADDRESS");
    });
    expect(w.find(".docs__params").exists()).toBe(false);
    expect(api.getParams).not.toHaveBeenCalled();
    expect(useAppStore().docsSection).toBe("parameters");
    w.unmount();
  });
});
