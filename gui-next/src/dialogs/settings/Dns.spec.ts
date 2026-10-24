// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { enableAutoUnmount, flushPromises } from "@vue/test-utils";
import { mountWithApp } from "@/test/mount";
import Dns from "./Dns.vue";
const api = vi.hoisted(() => ({ getDnsList: vi.fn(), putDnsList: vi.fn() }));
vi.mock("@/api", () => api);
enableAutoUnmount(afterEach);
beforeEach(() => {
  vi.resetAllMocks();
  api.getDnsList.mockResolvedValue({
    internal: ["223.5.5.5->direct", "127.0.0.1:53->direct"],
    external: ["https://dns.example/dns-query->proxy"],
  });
  api.putDnsList.mockResolvedValue(undefined);
});
const save = async (w: ReturnType<typeof mountWithApp>) => {
  await w
    .findAll("button")
    .find((b) => b.text() === "Save")!
    .trigger("click");
  await flushPromises();
};
describe("local DNS lists", () => {
  test("loads arrays and submits the exact internal/external text contract", async () => {
    const w = mountWithApp(Dns);
    await flushPromises();
    expect(
      w.get<HTMLTextAreaElement>('textarea[name="internal"]').element.value,
    ).toBe("223.5.5.5->direct\n127.0.0.1:53->direct");
    await w
      .get('textarea[name="external"]')
      .setValue("8.8.8.8->proxy\nhttps://dns.example/dns-query->proxy");
    await save(w);
    expect(api.putDnsList).toHaveBeenCalledExactlyOnceWith({
      internal: "223.5.5.5->direct\n127.0.0.1:53->direct",
      external: "8.8.8.8->proxy\nhttps://dns.example/dns-query->proxy",
    });
    expect(w.emitted("close")).toEqual([[true]]);
  });
  test("preserves rejected text and stays open", async () => {
    api.putDnsList.mockRejectedValue(new Error("invalid DNS server"));
    const w = mountWithApp(Dns);
    await flushPromises();
    await w.get('textarea[name="internal"]').setValue("my invalid draft");
    await save(w);
    expect(
      w.get<HTMLTextAreaElement>('textarea[name="internal"]').element.value,
    ).toBe("my invalid draft");
    expect(w.text()).toContain("invalid DNS server");
    expect(w.emitted("close")).toBeUndefined();
  });
  test("cannot overwrite existing DNS after a failed load", async () => {
    api.getDnsList.mockRejectedValue(new Error("offline"));
    const w = mountWithApp(Dns);
    await flushPromises();
    await save(w);
    expect(api.putDnsList).not.toHaveBeenCalled();
    expect(w.text()).toContain("offline");
  });
});
