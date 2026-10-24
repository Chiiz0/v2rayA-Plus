// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { VAutocomplete, VBtn, VSelect, VTextField } from "vuetify/components";
import { mountWithApp } from "@/test/mount";
import type * as Api from "@/api";
import type { DockerProxy } from "@/api/types";
import { closeAllNotices } from "@/composables/useNotify";
import DockerProxyDialog from "./DockerProxy.vue";
const api = vi.hoisted(() => ({
  getTouch: vi.fn(),
  postDockerProxy: vi.fn(),
  putDockerProxy: vi.fn(),
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof Api>()),
  ...api,
}));
const proxy: DockerProxy = {
  port: 10878,
  which: { _type: "subscriptionServer", id: 2, sub: 0 },
  frontWhich: { _type: "server", id: 1 },
  core: "xray",
  serverName: "Target",
  frontServerName: "Front",
  containerName: "container",
  status: "running",
};
let wrapper: VueWrapper;
beforeEach(() => {
  vi.clearAllMocks();
  closeAllNotices();
  api.getTouch.mockResolvedValue({
    running: false,
    touch: {
      servers: [
        { id: 1, _type: "server", name: "Front", address: "front:443" },
      ],
      subscriptions: [
        {
          id: 1,
          remarks: "Subscription",
          host: "sub.test",
          servers: [
            {
              id: 2,
              _type: "subscriptionServer",
              name: "Target",
              address: "target:443",
            },
          ],
        },
      ],
      connectedServer: [],
    },
  });
  api.postDockerProxy.mockResolvedValue({});
  api.putDockerProxy.mockResolvedValue({});
});
afterEach(() => wrapper?.unmount());
const save = async () => {
  wrapper
    .findAllComponents(VBtn)
    .find((b) => b.props("color") === "primary")!
    .vm.$emit("click");
  await flushPromises();
};

test("create validates the port and sends selected zero-based node identities", async () => {
  wrapper = mountWithApp(DockerProxyDialog);
  await flushPromises();
  const [target, front] = wrapper.findAllComponents(VAutocomplete);
  target.vm.$emit("update:modelValue", "subscriptionServer:0:2");
  front.vm.$emit("update:modelValue", "server:1");
  wrapper
    .findAllComponents(VSelect)
    .find((f) =>
      (f.props("items") as { value: string }[]).some((i) => i.value === "auto"),
    )!
    .vm.$emit("update:modelValue", "v2fly");
  const port = wrapper
    .findAllComponents(VTextField)
    .find((f) => f.props("type") === "number")!;
  for (const invalid of ["0", "65536", "1.5", "-1"]) {
    port.vm.$emit("update:modelValue", invalid);
    await save();
    expect(api.postDockerProxy).not.toHaveBeenCalled();
  }
  port.vm.$emit("update:modelValue", "10878");
  await save();
  expect(api.postDockerProxy).toHaveBeenCalledWith({
    port: 10878,
    core: "v2fly",
    which: proxy.which,
    frontWhich: proxy.frontWhich,
  });
  expect(wrapper.emitted("close")).toEqual([[true]]);
});

test("editing locks the port, removes a front proxy explicitly, and preserves failed draft for retry", async () => {
  api.putDockerProxy.mockRejectedValueOnce(new Error("container busy"));
  wrapper = mountWithApp(DockerProxyDialog, { props: { proxy } });
  await flushPromises();
  expect(
    wrapper
      .findAllComponents(VTextField)
      .find((f) => f.props("type") === "number")!
      .props("readonly"),
  ).toBe(true);
  wrapper
    .findAllComponents(VAutocomplete)[1]
    .vm.$emit("update:modelValue", null);
  wrapper
    .findAllComponents(VSelect)
    .find((f) =>
      (f.props("items") as { value: string }[]).some((i) => i.value === "auto"),
    )!
    .vm.$emit("update:modelValue", "auto");
  await save();
  expect(api.putDockerProxy).toHaveBeenCalledWith({
    port: 10878,
    core: "auto",
    which: proxy.which,
    frontWhich: null,
  });
  expect(wrapper.emitted("close")).toBeUndefined();
  expect(
    wrapper
      .findAllComponents(VSelect)
      .find((f) =>
        (f.props("items") as { value: string }[]).some(
          (i) => i.value === "auto",
        ),
      )!
      .props("modelValue"),
  ).toBe("auto");
  expect(
    wrapper.findAllComponents(VAutocomplete)[1].props("modelValue"),
  ).toBeNull();
  await save();
  expect(api.putDockerProxy).toHaveBeenCalledTimes(2);
  expect(wrapper.emitted("close")).toEqual([[true]]);
});

test("an unavailable node or a front proxy equal to the target cannot save", async () => {
  wrapper = mountWithApp(DockerProxyDialog, { props: { proxy } });
  await flushPromises();
  const [target, front] = wrapper.findAllComponents(VAutocomplete);
  front.vm.$emit("update:modelValue", "subscriptionServer:0:2");
  await save();
  expect(api.putDockerProxy).not.toHaveBeenCalled();
  front.vm.$emit("update:modelValue", null);
  target.vm.$emit("update:modelValue", "server:99");
  await save();
  expect(api.putDockerProxy).not.toHaveBeenCalled();
});
