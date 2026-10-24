<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { getDnsList, putDnsList } from "@/api";
import { errorText } from "@/api/errors";
import { useNotify } from "@/composables";

defineOptions({ name: "DnsDialog" });
const emit = defineEmits<{ close: [saved?: boolean] }>();
const { t, locale } = useI18n();
const zh = computed(() => locale.value.startsWith("zh"));
const notify = useNotify();
const dns = reactive({ internal: "", external: "" });
const ready = ref(false);
const saving = ref(false);
const error = ref("");

onMounted(async () => {
  try {
    const loaded = await getDnsList();
    dns.internal = loaded.internal.join("\n");
    dns.external = loaded.external.join("\n");
    ready.value = true;
  } catch (err) {
    error.value = errorText(err);
  }
});
async function save() {
  if (!ready.value || saving.value) return;
  saving.value = true;
  error.value = "";
  try {
    await putDnsList({ ...dns });
    notify.success(t("setting.saved"));
    emit("close", true);
  } catch (err) {
    error.value = errorText(err);
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <v-card>
    <v-card-title class="px-6 pt-6">{{ t("dns.title") }}</v-card-title>
    <v-card-text class="px-6">
      <p class="mb-4">
        {{
          zh
            ? "每行使用“服务器地址->出口”格式，例如 223.5.5.5->direct 或 https://dns.google/dns-query->proxy。列表用于“DNS 防污染 → 自定义 DNS”；保存列表后，在设置页保存并应用。保留已有服务器语法，由后端校验。"
            : "Use server->outbound on each line, for example 223.5.5.5->direct or https://dns.google/dns-query->proxy. These lists are used by “DNS anti-pollution → Custom DNS lists”. After saving, save and apply Settings. Existing server syntax is preserved and validated by the backend."
        }}
      </p>
      <v-textarea
        v-model="dns.internal"
        name="internal"
        :label="zh ? '国内查询 DNS 服务器' : 'Internal DNS servers'"
        :disabled="!ready || saving"
        rows="4"
        dir="ltr"
        spellcheck="false"
      />
      <v-textarea
        v-model="dns.external"
        name="external"
        :label="zh ? '国外查询 DNS 服务器' : 'External DNS servers'"
        :disabled="!ready || saving"
        rows="4"
        dir="ltr"
        spellcheck="false"
      />
      <v-alert v-if="error" type="error" variant="tonal" class="mt-2">{{
        error
      }}</v-alert>
    </v-card-text>
    <v-card-actions class="px-6 pb-4">
      <v-spacer />
      <v-btn variant="text" :disabled="saving" @click="emit('close')">{{
        t("operations.cancel")
      }}</v-btn>
      <v-btn
        color="primary"
        variant="flat"
        :disabled="!ready"
        :loading="saving"
        @click="save"
        >{{ t("operations.save") }}</v-btn
      >
    </v-card-actions>
  </v-card>
</template>
