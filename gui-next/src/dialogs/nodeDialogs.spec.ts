// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { VSelect, VSwitch, VTextField } from "vuetify/components";
import { Base64 } from "js-base64";
import { mountWithApp } from "@/test/mount";
import { noticeState, closeAllNotices } from "@/composables/useNotify";
import { generateShareLink, parseShareLink } from "@/lib/serverCodec";
import ImportDialog from "./Import.vue";
import SubscriptionDialog from "./Subscription.vue";
import ServerDialog from "./Server/index.vue";
import SharingDialog from "./Sharing.vue";

const api = vi.hoisted(() => ({
  postImport: vi.fn(),
  getSharingAddress: vi.fn(),
  patchSubscription: vi.fn(),
}));
const lib = vi.hoisted(() => ({ copyText: vi.fn(), toCanvas: vi.fn() }));
vi.mock("@/api", () => api);
vi.mock("@/lib/clipboard", () => ({ copyText: lib.copyText }));
vi.mock("qrcode", () => ({ default: { toCanvas: lib.toCanvas } }));
let wrapper: VueWrapper;
beforeEach(() => {
  vi.clearAllMocks();
  closeAllNotices();
  api.postImport.mockResolvedValue({});
  api.patchSubscription.mockResolvedValue({});
});
afterEach(() => wrapper?.unmount());
const clickButton = async (text: string) => {
  await wrapper
    .findAll("button")
    .find((b) => b.text() === text)!
    .trigger("click");
  await flushPromises();
};

test("server import preserves raw advanced links and marks HTTP URLs as nodes", async () => {
  wrapper = mountWithApp(ImportDialog, { props: { kind: "server" } });
  const source =
    "vless://id@example.com:443?type=xhttp&extra=%7B%22n%22%3A9007199254740993%7D\nhttps://user:pwd@example.com:443#proxy";
  await wrapper.get("textarea").setValue(source);
  await clickButton("Import");
  expect(api.postImport).toHaveBeenCalledWith({
    url: source.replace("https://", "https-proxy://"),
    kind: "server",
  });
  expect(wrapper.emitted("close")).toEqual([[true]]);
});

test("failed subscription import retains its draft and an unmodified URL", async () => {
  api.postImport.mockRejectedValue(new Error("offline"));
  wrapper = mountWithApp(ImportDialog, { props: { kind: "subscription" } });
  await wrapper.get("textarea").setValue("https://example.com/sub?token=a=b");
  await clickButton("Import");
  expect(api.postImport).toHaveBeenCalledWith({
    url: "https://example.com/sub?token=a=b",
    kind: "subscription",
  });
  expect(wrapper.get("textarea").element.value).toBe(
    "https://example.com/sub?token=a=b",
  );
  expect(wrapper.emitted("close")).toBeUndefined();
  expect(noticeState.current?.text).toContain("offline");
});

test("subscription edit submits address, remark and auto-select while leaving original untouched", async () => {
  const original = {
    id: 2,
    _type: "subscription" as const,
    address: "https://old.test/sub",
    remarks: "old",
    host: "old.test",
    servers: [],
    status: "",
    info: "",
    autoSelect: false,
  };
  wrapper = mountWithApp(SubscriptionDialog, {
    props: { subscription: original },
  });
  await wrapper.get("textarea").setValue("https://new.test/sub");
  wrapper.getComponent(VTextField).vm.$emit("update:modelValue", "new");
  wrapper.getComponent(VSwitch).vm.$emit("update:modelValue", true);
  await clickButton("Save and Apply");
  expect(api.patchSubscription).toHaveBeenCalledWith({
    subscription: {
      ...original,
      address: "https://new.test/sub",
      remarks: "new",
      autoSelect: true,
    },
  });
  expect(original.remarks).toBe("old");
  expect(wrapper.emitted("close")).toEqual([[true]]);
});

test("SSR edit loads real fields, saves the same node and hides unsupported protocols", async () => {
  const source = {
    protocol: "ssr",
    server: "example.com",
    port: "443",
    method: "aes-128-cfb",
    proto: "origin",
    obfs: "plain",
    name: "old",
    password: "pwd",
  };
  api.getSharingAddress.mockResolvedValue({
    sharingAddress: generateShareLink(source),
  });
  wrapper = mountWithApp(ServerDialog, {
    props: { which: { id: 4, _type: "server" } },
  });
  await flushPromises();
  const protocol = wrapper.findAllComponents(VSelect)[0];
  expect(
    (protocol.props("items") as { value: string }[]).map((i) => i.value),
  ).toEqual([
    "vmess",
    "vless",
    "ss",
    "ssr",
    "trojan",
    "juicity",
    "tuic",
    "http",
    "socks5",
  ]);
  wrapper
    .findAllComponents(VTextField)
    .find((f) => f.props("label") === "Servername")!
    .vm.$emit("update:modelValue", "renamed");
  await clickButton("Save and Apply");
  expect(api.postImport.mock.calls[0][0].which).toEqual({
    id: 4,
    _type: "server",
  });
  expect(parseShareLink(api.postImport.mock.calls[0][0].url)).toMatchObject({
    ...source,
    name: "renamed",
  });
  expect(wrapper.emitted("close")).toEqual([[true]]);
});

test("a malformed node closes without importing defaults", async () => {
  api.getSharingAddress.mockResolvedValue({
    sharingAddress: "vless://id@example.com:443?type=xhttp&extra=%5B%5D",
  });
  wrapper = mountWithApp(ServerDialog, {
    props: { which: { id: 1, _type: "server" } },
  });
  await flushPromises();
  expect(wrapper.emitted("close")).toEqual([[]]);
  expect(api.postImport).not.toHaveBeenCalled();
});

test("sharing uses the backend link unchanged, and wraps subscription QR data only", async () => {
  const link = "https://example.com/sub?token=a=b";
  wrapper = mountWithApp(SharingDialog, {
    props: { title: "Share", name: "Sub", link, type: "subscription" },
  });
  expect(lib.toCanvas.mock.calls[0][1]).toBe("sub://" + Base64.encode(link));
  wrapper.getComponent(VTextField).vm.$emit("click:appendInner");
  await flushPromises();
  expect(lib.copyText).toHaveBeenCalledWith(link);
});
