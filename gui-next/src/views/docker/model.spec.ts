// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, test } from "vitest";
import { defineComponent, h } from "vue";
import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { mountWithApp } from "@/test/mount";
import { client } from "@/api/client";
import {
  dialogState,
  closeDialog,
  closeAllDialogs,
} from "@/composables/useDialog";
import { closeAllNotices } from "@/composables/useNotify";
import type { DockerProxy, Touch } from "@/api/types";
import { nodeOptions } from "./nodeOptions";
import { useDockerProxies } from "./model";

const adapter = client.defaults.adapter;
const proxy: DockerProxy = {
  port: 10878,
  which: { _type: "subscriptionServer", id: 2, sub: 0 },
  frontWhich: { _type: "server", id: 1 },
  core: "xray",
  serverName: "Node",
  frontServerName: "Front",
  containerName: "v2raya-socks5-10878",
  status: "running",
};
let wrapper: VueWrapper;
let model: ReturnType<typeof useDockerProxies>;
let rows: DockerProxy[] | null;
let fail = false;
let sent: {
  method: string;
  path: string;
  data: unknown;
  params: unknown;
  timeout: number;
}[];
beforeEach(() => {
  closeAllNotices();
  rows = [structuredClone(proxy)];
  sent = [];
  fail = false;
  client.defaults.adapter = async (config) => {
    const path = new URL(config.url!, "http://localhost").pathname;
    const data = config.data ? JSON.parse(config.data) : undefined;
    sent.push({
      method: config.method!,
      path,
      data,
      params: config.params,
      timeout: config.timeout!,
    });
    if (fail) throw new Error("docker unavailable");
    if (config.method === "delete") rows = [];
    if (path.endsWith("/pause")) rows![0].status = "paused";
    if (path.endsWith("/resume")) rows![0].status = "running";
    return {
      config,
      status: 200,
      statusText: "OK",
      headers: {},
      data: {
        code: "SUCCESS",
        data: path.endsWith("httpLatency")
          ? { proxies: [{ port: 10878, pingLatency: "42ms" }] }
          : rows,
      },
    };
  };
  wrapper = mountWithApp(
    defineComponent({
      setup() {
        model = useDockerProxies();
        return () => h("div");
      },
    }),
  );
});
afterEach(() => {
  wrapper.unmount();
  client.defaults.adapter = adapter;
  closeAllDialogs();
});
const confirm = async (value: boolean) => {
  await flushPromises();
  closeDialog(dialogState.stack.at(-1)!.id, value);
};

test("Docker list normalizes an empty backend and retains the prior list on failure", async () => {
  rows = null;
  await model.sync();
  expect(model.proxies.value).toEqual([]);
  rows = [structuredClone(proxy)];
  await model.sync();
  expect(model.proxies.value[0].which.sub).toBe(0);
  fail = true;
  await model.sync();
  expect(model.loadError.value).toContain("docker unavailable");
  expect(model.proxies.value).toHaveLength(1);
});

test("pause, resume and delete require confirmation and refresh real API state", async () => {
  await model.sync();
  const cancelled = model.action(proxy, "remove");
  await confirm(false);
  await cancelled;
  expect(sent.filter((r) => r.method !== "get")).toHaveLength(0);
  for (const action of ["pause", "resume", "remove"] as const) {
    const request = model.action(proxy, action);
    await confirm(true);
    await request;
  }
  expect(sent.filter((r) => r.method !== "get")).toEqual([
    {
      method: "put",
      path: "/api/dockerProxy/pause",
      data: { port: 10878 },
      params: undefined,
      timeout: 180000,
    },
    {
      method: "put",
      path: "/api/dockerProxy/resume",
      data: { port: 10878 },
      params: undefined,
      timeout: 180000,
    },
    {
      method: "delete",
      path: "/api/dockerProxy",
      data: { port: 10878 },
      params: undefined,
      timeout: 180000,
    },
  ]);
  expect(model.proxies.value).toEqual([]);
});

test("HTTP latency targets running containers and sends JSON-array ports", async () => {
  await model.test([proxy, { ...proxy, port: 10879, status: "paused" }]);
  expect(sent[0]).toMatchObject({
    method: "get",
    path: "/api/dockerProxy/httpLatency",
    params: { ports: [10878] },
    timeout: 180000,
  });
  expect(model.latencies.value[10878]).toBe("42ms");
  expect(model.testing.value).toBe(false);
});

test("node choices address subscription indices from zero independently of subscription IDs", () => {
  const touch = {
    servers: [{ id: 7, _type: "server", name: "Local", address: "localhost" }],
    subscriptions: [
      {
        id: 1,
        remarks: "A",
        host: "a",
        servers: [{ id: 2, name: "A node", address: "a:443" }],
      },
      {
        id: 2,
        remarks: "B",
        host: "b",
        servers: [{ id: 1, name: "B node", address: "b:443" }],
      },
    ],
  } as Touch;
  expect(nodeOptions(touch).map((o) => o.which)).toEqual([
    { id: 7, _type: "server" },
    { id: 2, _type: "subscriptionServer", sub: 0 },
    { id: 1, _type: "subscriptionServer", sub: 1 },
  ]);
});
