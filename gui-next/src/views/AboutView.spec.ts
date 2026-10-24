// @vitest-environment happy-dom
import { describe, expect, test } from "vitest";
import { nextTick } from "vue";
import { useAppStore } from "@/stores/app";
import type { VersionResponse } from "@/api/types";
import { mountWithApp } from "@/test/mount";
import About from "./AboutView.vue";

describe("the about dialog", () => {
  test("shows the actual database backend and a classic interface link", async () => {
    const w = mountWithApp(About);
    expect(w.find('[data-testid="database-backend"]').exists()).toBe(false);
    useAppStore().version = {
      version: "test",
      databaseBackend: "bolt",
    } as VersionResponse;
    await nextTick();
    expect(w.get('[data-testid="database-backend"]').text()).toContain("bbolt");
    useAppStore().version!.databaseBackend = "sqlite";
    await nextTick();
    expect(w.get('[data-testid="database-backend"]').text()).toContain(
      "SQLite",
    );
    expect(w.find('a[href="./legacy/"]').exists()).toBe(true);
    w.unmount();
  });
  test("identifies the fork and links its issue tracker", () => {
    const w = mountWithApp(About);
    expect(w.text()).toContain("A proxy management panel based on v2rayA");
    expect(w.text()).toContain("Founded by @mzz2017");
    expect(
      w.find('a[href="https://github.com/Chiiz0/V2rayA-Plus/issues"]').exists(),
    ).toBe(true);
    w.unmount();
  });
});
