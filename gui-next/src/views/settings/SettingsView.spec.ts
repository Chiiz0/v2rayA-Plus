// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { DOMWrapper, flushPromises, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import { VApp, VMain } from "vuetify/components";
import {
  getDnsList,
  getRemoteGFWListVersion,
  getSetting,
  putSetting,
} from "@/api";
import type { Setting, VersionResponse } from "@/api/types";
import DialogHost from "@/components/hosts/DialogHost.vue";
import { closeAllDialogs } from "@/composables/useDialog";
import { useAppStore } from "@/stores/app";
import { mountWithApp } from "@/test/mount";
import SettingsView from "../SettingsView.vue";

vi.mock("@/api", () => ({
  getSetting: vi.fn(),
  getRemoteGFWListVersion: vi.fn(),
  putSetting: vi.fn(),
  getTouch: vi.fn().mockResolvedValue({ running: false }),
  getDnsList: vi.fn(),
  putDnsList: vi.fn(),
}));

const loaded: Setting = {
  transparent: "proxy",
  transparentType: "tproxy",
  ipforward: true,
  portSharing: false,
  tproxyExcludedInterfaces: "docker*,veth*",
  tunAutoRoute: true,
  tunRouteShellType: "bash",
  tunRouteShellPath: "/bin/bash",
  tunSetupScript: "ip route add default dev tun0",
  tunTeardownScript: "ip route del default dev tun0",
  tunExcludeProcesses: "firefox,chrome",
  pacMode: "gfwlist",
  pacAutoUpdateMode: "auto_update_at_intervals",
  pacAutoUpdateIntervalHour: 24,
  subscriptionAutoUpdateMode: "auto_update_at_intervals",
  subscriptionAutoUpdateIntervalHour: 12,
  proxyModeWhenSubscribe: "direct",
  tcpFastOpen: "default",
  logLevel: "info",
  inboundSniffing: "http,tls",
  routeOnly: true,
  muxOn: "yes",
  mux: 8,
};

let wrapper: VueWrapper;
beforeEach(() => {
  vi.mocked(getSetting).mockResolvedValue({
    // the DNS mode is the service's, and the settings page never sends it back
    setting: {
      ...loaded,
      specialMode: "supervisor",
      antipollution: "advanced",
      dnsMode: "service",
    },
    localGFWListVersion: "2026-09-15",
    localGeositeVersion: "",
  });
  vi.mocked(getRemoteGFWListVersion).mockResolvedValue({
    remoteGFWListVersion: "2026-09-15",
  });
  vi.mocked(putSetting).mockResolvedValue(undefined);
  vi.mocked(getDnsList).mockResolvedValue({
    internal: ["223.5.5.5"],
    external: [],
  });
});
afterEach(() => {
  closeAllDialogs();
  wrapper?.unmount();
  vi.clearAllMocks();
});

async function mountPage() {
  wrapper = mountWithApp(
    defineComponent({
      setup: () => () =>
        h(VApp, null, () => [
          h(VMain, null, () => h(SettingsView)),
          h(DialogHost),
        ]),
    }),
  );
  useAppStore().version = {
    os: "linux",
    isRoot: true,
    tunSupported: true,
  } as VersionResponse;
  await flushPromises();
}

async function choose(label: string, option: string) {
  await wrapper.get(`button[aria-label^="${label}:"]`).trigger("click");
  await flushPromises();
  const item = [...document.querySelectorAll('[role="menuitemradio"]')].find(
    (entry) => entry.textContent?.trim() === option,
  );
  expect(item, `${label}: ${option}`).toBeDefined();
  await new DOMWrapper(item!).trigger("click");
  await flushPromises();
}

async function toggle(label: string, value: boolean) {
  await wrapper.get(`input[aria-label="${label}"]`).setValue(value);
  await flushPromises();
}

describe("settings list", () => {
  test("applies separately saved DNS and routing even when no setting field changed", async () => {
    await mountPage();
    await wrapper.get('button[type="submit"]').trigger("click");
    await flushPromises();
    expect(putSetting).toHaveBeenCalledExactlyOnceWith(
      {},
      { signal: expect.any(AbortSignal) },
    );
  });
  test("edits settings through the list and preserves the save request contract", async () => {
    await mountPage();
    expect(wrapper.find('button[aria-label^="Log Level:"]').exists()).toBe(
      false,
    );
    await wrapper
      .get('input[aria-label="Update GFWList Regularly (Unit: hour)"]')
      .setValue("48");
    await wrapper.get('input[aria-label="Concurrency"]').setValue("16");
    // the dashboard's quick controls are here too, in the same form
    await toggle("Port Sharing", true);
    await choose("Transparent Proxy/System Proxy", "Off");
    expect(
      wrapper.find('input[aria-label="Excluded Interface Prefixes"]').exists(),
    ).toBe(false);
    await wrapper.get('button[type="submit"]').trigger("click");
    await flushPromises();
    expect(putSetting).toHaveBeenCalledExactlyOnceWith(
      {
        transparent: "close",
        portSharing: true,
        pacAutoUpdateIntervalHour: 48,
        mux: 16,
      },
      { signal: expect.any(AbortSignal) },
    );
  });

  test("shows dependent rows and blocks saving invalid intervals", async () => {
    await mountPage();
    await choose("Sniffing", "Off");
    expect(wrapper.find('input[aria-label="RouteOnly"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain("Domains Excluded");
    await toggle("Multiplex", false);
    expect(wrapper.find('input[aria-label="Concurrency"]').exists()).toBe(
      false,
    );
    await wrapper
      .get('input[aria-label="Update GFWList Regularly (Unit: hour)"]')
      .setValue("0");
    await wrapper.get('button[type="submit"]').trigger("click");
    await flushPromises();
    expect(putSetting).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("Required");
    await wrapper
      .get('input[aria-label="Update GFWList Regularly (Unit: hour)"]')
      .setValue("12");
    await wrapper.get('button[type="submit"]').trigger("click");
    await flushPromises();
    expect(putSetting).toHaveBeenCalledOnce();
  });

  test("uses legacy DNS lists without sending upstream-only settings", async () => {
    await mountPage();
    const row = wrapper
      .findAll(".v-list-item")
      .find((item) => item.text().startsWith("DNS Settings"));
    await row!.trigger("click");
    await flushPromises();
    const dialog = new DOMWrapper(document.querySelector('[role="dialog"]')!);
    expect(
      dialog.get<HTMLTextAreaElement>('textarea[name="internal"]').element
        .value,
    ).toBe("223.5.5.5");
    closeAllDialogs();
    await toggle("Port Sharing", true);
    await wrapper.get('button[type="submit"]').trigger("click");
    await flushPromises();
    expect(putSetting).toHaveBeenCalledExactlyOnceWith(
      { portSharing: true },
      { signal: expect.any(AbortSignal) },
    );
  });

  test("retains rejected edits and preserves existing DNS/special mode values", async () => {
    vi.mocked(putSetting).mockRejectedValueOnce(
      new Error("backend rejected settings"),
    );
    await mountPage();
    await wrapper.get('input[aria-label="Concurrency"]').setValue("16");
    await wrapper.get('button[type="submit"]').trigger("click");
    await flushPromises();
    expect(
      wrapper.get<HTMLInputElement>('input[aria-label="Concurrency"]').element
        .value,
    ).toBe("16");
    expect(getSetting).toHaveBeenCalledOnce();
    expect(putSetting).toHaveBeenCalledExactlyOnceWith(
      { mux: 16 },
      { signal: expect.any(AbortSignal) },
    );
  });

  test("opens the existing About content as a dismissible dialog", async () => {
    await mountPage();
    const about = wrapper
      .findAll(".v-list-item")
      .find((row) => row.find(".v-list-item-title").text() === "About");
    await about!.trigger("click");
    await flushPromises();
    const dialog = new DOMWrapper(document.querySelector('[role="dialog"]')!);
    expect(dialog.text()).toContain("Founded by @mzz2017");
    expect(dialog.find('a[href$="/issues"]').exists()).toBe(true);
    const close = dialog
      .findAll("button")
      .find((button) => button.text() === "Close");
    await close!.trigger("click");
    await flushPromises();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  test("shows the standard GeoSite date without treating it as a GFWList version", async () => {
    vi.mocked(getSetting).mockResolvedValue({
      setting: { ...loaded },
      localGFWListVersion: "",
      localGeositeVersion: "2026-09-16",
    });
    vi.mocked(getRemoteGFWListVersion).mockResolvedValue({
      remoteGFWListVersion: "2026-09-15",
    });
    await mountPage();
    expect(wrapper.text()).toContain("Local: 2026-09-16 (geosite)");
    expect(wrapper.text()).not.toContain(
      "Based on modified time of file which sometimes is after latest version online.",
    );
  });
});
