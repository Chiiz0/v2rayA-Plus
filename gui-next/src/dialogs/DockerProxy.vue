<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { getTouch, postDockerProxy, putDockerProxy } from "@/api";
import type {
  DockerProxy,
  DockerProxyCore,
  DockerProxyPayload,
} from "@/api/types";
import { errorText } from "@/api/errors";
import { useNotify } from "@/composables";
import {
  nodeKey,
  nodeOptions,
  type NodeOption,
} from "@/views/docker/nodeOptions";

const props = defineProps<{ proxy?: DockerProxy }>();
const emit = defineEmits<{ close: [saved?: boolean] }>();
const { t } = useI18n();
const notify = useNotify();
const port = ref<number | string>(props.proxy?.port ?? "");
const core = ref<DockerProxyCore>(props.proxy?.core || "auto");
const target = ref(nodeKey(props.proxy?.which));
const front = ref<string | null>(nodeKey(props.proxy?.frontWhich) || null);
const options = ref<NodeOption[]>([]);
const loading = ref(true);
const loadError = ref("");
const saving = ref(false);
const form = ref<{ validate(): Promise<{ valid: boolean }> } | null>(null);
const coreOptions = computed(() => [
  { value: "auto", title: t("docker.auto") },
  { value: "v2fly", title: "V2Fly" },
  { value: "xray", title: "Xray" },
]);
const validPort = (value: unknown) =>
  (/^\d+$/.test(String(value)) &&
    Number(value) >= 1 &&
    Number(value) <= 65535) ||
  t("docker.portInvalid");
const validNode = (value: unknown) =>
  options.value.some((o) => o.value === value) || t("docker.nodeRequired");
const validFront = (value: unknown) =>
  !value ||
  (value !== target.value && validNode(value)) ||
  t("docker.differentNodes");
async function load() {
  loading.value = true;
  loadError.value = "";
  try {
    options.value = nodeOptions((await getTouch()).touch, t("docker.local"));
  } catch (err) {
    loadError.value = errorText(err);
  } finally {
    loading.value = false;
  }
}
onMounted(load);
async function save() {
  if (saving.value || loading.value || loadError.value) return;
  if (!(await form.value?.validate())?.valid) return;
  const which = options.value.find((o) => o.value === target.value)?.which;
  if (!which) return;
  const frontWhich =
    options.value.find((o) => o.value === front.value)?.which ?? null;
  const payload: DockerProxyPayload = {
    port: Number(port.value),
    core: core.value,
    which,
    frontWhich,
  };
  saving.value = true;
  try {
    await (props.proxy ? putDockerProxy(payload) : postDockerProxy(payload));
    notify.success(t("docker.saved"));
    emit("close", true);
  } catch (err) {
    notify.error(t("docker.failed", { message: errorText(err) }));
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <v-card>
    <v-card-title class="px-6 pt-6">{{
      t(proxy ? "docker.edit" : "docker.create")
    }}</v-card-title>
    <v-card-text>
      <v-alert v-if="loadError" type="error" variant="tonal" class="mb-4">
        {{ t("docker.nodesFailed", { message: loadError }) }}
        <v-btn variant="text" @click="load">{{ t("docker.retry") }}</v-btn>
      </v-alert>
      <v-progress-linear v-if="loading" indeterminate class="mb-4" />
      <v-alert
        v-else-if="!loadError && !options.length"
        type="info"
        variant="tonal"
        class="mb-4"
        >{{ t("docker.noNodes") }}</v-alert
      >
      <v-form ref="form" :disabled="saving" @submit.prevent="save">
        <v-autocomplete
          v-model="target"
          :items="options"
          :label="t('docker.target')"
          :rules="[validNode]"
          :disabled="loading || !!loadError"
          auto-select-first
        />
        <v-row density="compact">
          <v-col cols="12" sm="6"
            ><v-text-field
              v-model="port"
              type="number"
              min="1"
              max="65535"
              :label="t('docker.port')"
              :rules="[validPort]"
              :readonly="!!proxy"
              :hint="proxy ? t('docker.unchangedPort') : ''"
              :persistent-hint="!!proxy"
              dir="ltr"
          /></v-col>
          <v-col cols="12" sm="6"
            ><v-select
              v-model="core"
              :items="coreOptions"
              :label="t('docker.core')"
          /></v-col>
        </v-row>
        <v-autocomplete
          v-model="front"
          :items="options"
          :label="t('docker.front')"
          :placeholder="t('docker.noFront')"
          :rules="[validFront]"
          :disabled="loading || !!loadError"
          clearable
        />
      </v-form>
    </v-card-text>
    <v-card-actions class="px-6 pb-4">
      <v-spacer />
      <v-btn :disabled="saving" @click="emit('close')">{{
        t("docker.cancel")
      }}</v-btn>
      <v-btn
        variant="flat"
        color="primary"
        :loading="saving"
        :disabled="loading || !!loadError || !options.length"
        @click="save"
        >{{ t("docker.save") }}</v-btn
      >
    </v-card-actions>
  </v-card>
</template>
