// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { enableAutoUnmount, flushPromises } from "@vue/test-utils";
import { mountWithApp } from "@/test/mount";
import Domains from "./DomainsExcluded.vue";
import WhiteIps from "./TproxyWhiteIps.vue";
import Outbound from "../OutboundGroup.vue";
const api = vi.hoisted(() => ({
  getDomainsExcluded: vi.fn(),
  putDomainsExcluded: vi.fn(),
  getTproxyWhiteIpGroups: vi.fn(),
  putTproxyWhiteIpGroups: vi.fn(),
  getOutbound: vi.fn(),
  putOutbound: vi.fn(),
}));
vi.mock("@/api", () => api);
enableAutoUnmount(afterEach);
beforeEach(() => vi.resetAllMocks());
const save = async (w: ReturnType<typeof mountWithApp>) => {
  await w
    .findAll("button")
    .find((b) => b.text() === "Save")!
    .trigger("click");
  await flushPromises();
};
describe("local settings data protection", () => {
  test.each([
    [Domains, api.getDomainsExcluded, api.putDomainsExcluded, {}],
    [WhiteIps, api.getTproxyWhiteIpGroups, api.putTproxyWhiteIpGroups, {}],
    [Outbound, api.getOutbound, api.putOutbound, { outbound: "proxy" }],
  ] as const)(
    "does not save empty defaults after a failed load (%#)",
    async (component, get, put, props) => {
      get.mockRejectedValue(new Error("offline"));
      const w = mountWithApp(component, { props });
      await flushPromises();
      await save(w);
      expect(put).not.toHaveBeenCalled();
      expect(w.emitted("close")).toBeUndefined();
    },
  );
  test("keeps arbitrary existing country codes and CIDRs", async () => {
    api.getTproxyWhiteIpGroups.mockResolvedValue({
      countryCodes: ["JP", "CN"],
      customIps: ["2001:db8::/32", "192.0.2.0/24"],
    });
    const w = mountWithApp(WhiteIps);
    await flushPromises();
    expect(w.findAll(".v-chip").some((chip) => chip.text() === "JP")).toBe(
      true,
    );
    await save(w);
    expect(api.putTproxyWhiteIpGroups).toHaveBeenCalledWith({
      countryCodes: ["JP", "CN"],
      customIps: ["2001:db8::/32", "192.0.2.0/24"],
    });
  });
  test("sends only supported outbound settings and retains rejected input", async () => {
    api.getOutbound.mockResolvedValue({
      setting: {
        probeURL: "https://example.com/probe",
        probeInterval: "10s",
        type: "leastping",
        selected: "unsupported",
      },
    });
    api.putOutbound.mockRejectedValue(new Error("invalid interval"));
    const w = mountWithApp(Outbound, { props: { outbound: "proxy" } });
    await flushPromises();
    await w.findAll("input")[1]!.setValue("broken");
    await save(w);
    expect(api.putOutbound).toHaveBeenCalledWith({
      outbound: "proxy",
      setting: {
        probeURL: "https://example.com/probe",
        probeInterval: "broken",
        type: "leastping",
      },
    });
    expect(w.findAll<HTMLInputElement>("input")[1]!.element.value).toBe(
      "broken",
    );
    expect(w.emitted("close")).toBeUndefined();
  });
});
