import { ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  getDockerProxies,
  pauseDockerProxy,
  resumeDockerProxy,
  deleteDockerProxy,
  getDockerProxyHttpLatency,
} from "@/api";
import type { DockerProxy } from "@/api/types";
import { errorText } from "@/api/errors";
import { useConfirm, useDialog, useNotify } from "@/composables";
import DockerProxyDialog from "@/dialogs/DockerProxy.vue";

export function useDockerProxies() {
  const { t } = useI18n();
  const notify = useNotify();
  const confirm = useConfirm();
  const dialog = useDialog();
  const proxies = ref<DockerProxy[]>([]);
  const loading = ref(false);
  const busy = ref(false);
  const loadError = ref("");
  const testing = ref(false);
  const latencies = ref<Record<number, string>>({});

  async function sync() {
    if (loading.value) return;
    loading.value = true;
    loadError.value = "";
    try {
      proxies.value = await getDockerProxies();
    } catch (err) {
      loadError.value = errorText(err);
    } finally {
      loading.value = false;
    }
  }
  async function edit(proxy?: DockerProxy) {
    if (busy.value) return;
    if (
      await dialog.open<boolean>(DockerProxyDialog, { proxy }, { width: 640 })
        .result
    )
      await sync();
  }
  async function action(
    proxy: DockerProxy,
    kind: "pause" | "resume" | "remove",
  ) {
    if (busy.value) return;
    busy.value = true;
    try {
      if (
        !(await confirm({
          title: t(`docker.${kind}Title`),
          message: t(`docker.${kind}Message`, { port: proxy.port }),
          confirmText: t(`docker.${kind}`),
          destructive: kind === "remove",
        }))
      )
        return;
      if (kind === "pause") await pauseDockerProxy(proxy.port);
      if (kind === "resume") await resumeDockerProxy(proxy.port);
      if (kind === "remove") await deleteDockerProxy(proxy.port);
      delete latencies.value[proxy.port];
      await sync();
      notify.success(
        t(kind === "remove" ? "docker.deleted" : "docker.updated"),
      );
    } catch (err) {
      notify.error(t("docker.failed", { message: errorText(err) }));
    } finally {
      busy.value = false;
    }
  }
  async function test(targets: DockerProxy[] = proxies.value) {
    const ports = targets
      .filter((p) => p.status === "running")
      .map((p) => p.port);
    if (testing.value || !ports.length) return;
    testing.value = true;
    try {
      const result = await getDockerProxyHttpLatency(ports);
      for (const p of result.proxies) latencies.value[p.port] = p.pingLatency;
    } catch (err) {
      notify.error(t("docker.failed", { message: errorText(err) }));
    } finally {
      testing.value = false;
    }
  }
  return {
    proxies,
    loading,
    busy,
    loadError,
    testing,
    latencies,
    sync,
    edit,
    action,
    test,
  };
}
