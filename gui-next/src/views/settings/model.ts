// Only settings supported by the local service are editable here.
import { computed, reactive, ref } from "vue";
import {
  getRemoteGFWListVersion,
  getSetting,
  getTouch,
  putSetting,
} from "@/api";
import type { Setting } from "@/api/types";
import { openLoading } from "@/composables/useLoading";
import { useAppStore } from "@/stores/app";
import { runningOf } from "@/views/nodes/model";

export const defaultForm = () => ({
  transparent: "close",
  transparentType: "tproxy",
  ipforward: false,
  portSharing: false,
  pacMode: "whitelist",
  pacAutoUpdateMode: "none",
  pacAutoUpdateIntervalHour: 0,
  subscriptionAutoUpdateMode: "none",
  subscriptionAutoUpdateIntervalHour: 0,
  proxyModeWhenSubscribe: "direct",
  tcpFastOpen: "default",
  specialMode: "none",
  antipollution: "closed",
  inboundSniffing: "disable",
  routeOnly: false,
  muxOn: "no",
  mux: 8,
});
export type SettingForm = ReturnType<typeof defaultForm>;

/** what PUT /setting takes; the numbers are parsed, as the old dialog did */
export function toRequest(
  form: SettingForm,
  saved?: SettingForm,
): Partial<Setting> {
  const normalized = {
    ...form,
    pacAutoUpdateIntervalHour: parseInt(String(form.pacAutoUpdateIntervalHour)),
    subscriptionAutoUpdateIntervalHour: parseInt(
      String(form.subscriptionAutoUpdateIntervalHour),
    ),
    mux: parseInt(String(form.mux)),
  };
  const previous = saved ? toRequest(saved) : undefined;
  return Object.fromEntries(
    Object.entries(normalized).filter(
      ([key, value]) => !previous || value !== previous[key as keyof Setting],
    ),
  ) as Partial<Setting>;
}

export function useSettings() {
  const store = useAppStore();
  const form = reactive(defaultForm());
  const ready = ref(false);
  const saved = ref("");
  const dirty = computed(
    () => ready.value && JSON.stringify(form) !== saved.value,
  );
  const localGFWListVersion = ref("");
  const localGeositeVersion = ref("");
  const remoteGFWListVersion = ref("");

  async function load(): Promise<void> {
    const res = await getSetting();
    for (const key of Object.keys(form) as (keyof SettingForm)[]) {
      if (key in res.setting)
        (form as Record<string, unknown>)[key] = res.setting[key];
    }
    localGFWListVersion.value = res.localGFWListVersion ?? "";
    localGeositeVersion.value = res.localGeositeVersion ?? "";
    saved.value = JSON.stringify(form);
    ready.value = true;
  }

  async function loadRemoteVersion(): Promise<void> {
    remoteGFWListVersion.value = (
      await getRemoteGFWListVersion()
    ).remoteGFWListVersion;
  }

  async function loadLocalVersion(): Promise<void> {
    const res = await getSetting();
    localGFWListVersion.value = res.localGFWListVersion ?? "";
    localGeositeVersion.value = res.localGeositeVersion ?? "";
  }

  /** save applies the settings; an invalid config stops the core, which the store then shows. */
  async function save(): Promise<void> {
    if (!ready.value) return;
    const snapshot = JSON.stringify(form);
    const patch = toRequest(JSON.parse(snapshot), JSON.parse(saved.value));
    // An empty patch still applies DNS, routing and list edits saved in dialogs.
    const loading = openLoading();
    const control = new AbortController();
    try {
      await putSetting(patch, { signal: control.signal });
      saved.value = snapshot;
    } catch (err) {
      // Refresh the actual core status without discarding the rejected draft.
      await getTouch()
        .then((res) =>
          store.setRunning(
            runningOf(res.running, !!res.networkPaused),
            !!res.networkPaused,
          ),
        )
        .catch(() => {});
      throw err;
    } finally {
      loading.close();
    }
  }

  return {
    form,
    ready,
    dirty,
    localGFWListVersion,
    localGeositeVersion,
    remoteGFWListVersion,
    load,
    loadRemoteVersion,
    loadLocalVersion,
    save,
  };
}
