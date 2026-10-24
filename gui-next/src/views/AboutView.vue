<script setup lang="ts">
// About: what v2rayA is, its version, where the docs and the discussions are.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  mdiBookOpenPageVariantOutline,
  mdiForumOutline,
  mdiGithub,
} from "@mdi/js";
import { useAppStore } from "@/stores/app";

defineOptions({ name: "AboutView" });
const { t, locale } = useI18n();
const zh = computed(() => locale.value.startsWith("zh"));
const store = useAppStore();

const repo = "https://github.com/Chiiz0/V2rayA-Plus";
</script>

<template>
  <div class="about">
    <v-sheet color="surface-container-low" rounded="xl" class="pa-6 pa-sm-8">
      <h1 class="md3-headline-medium mb-2">v2rayA-Plus</h1>
      <p class="md3-body-large mb-1">
        {{
          zh
            ? "基于 v2rayA 的代理管理面板，支持 Xray 与 V2Ray 内核。"
            : "A proxy management panel based on v2rayA, supporting Xray and V2Ray cores."
        }}
      </p>
      <p class="md3-body-medium mb-1">
        VMess · VLESS · Trojan · Shadowsocks · SOCKS5 · HTTP · XHTTP
      </p>
      <p v-if="store.version" class="md3-body-medium text-on-surface-variant">
        {{ t("welcome.default", { version: store.version.version }) }}
      </p>
      <p
        v-if="store.version?.databaseBackend"
        class="md3-body-medium text-on-surface-variant"
        data-testid="database-backend"
      >
        {{ zh ? "当前数据库" : "Current database" }}:
        {{ store.version.databaseBackend === "sqlite" ? "SQLite" : "bbolt" }}
      </p>
      <p class="md3-body-medium text-on-surface-variant mt-2">
        {{ t("about.founded") }}
        {{ t("about.local") }}
        <a
          href="https://github.com/v2rayA/v2rayA"
          target="_blank"
          rel="noreferrer"
          >{{ zh ? "上游项目" : "Upstream project" }}</a
        >
      </p>
      <div class="d-flex flex-wrap ga-2 mt-6">
        <v-btn variant="tonal" href="./legacy/">{{
          zh ? "经典界面" : "Classic interface"
        }}</v-btn>
        <v-btn
          variant="tonal"
          :href="repo"
          target="_blank"
          rel="noreferrer"
          :prepend-icon="mdiGithub"
          >GitHub</v-btn
        >
        <v-btn
          variant="tonal"
          href="#docs"
          :prepend-icon="mdiBookOpenPageVariantOutline"
          >{{ t("common.docs") }}</v-btn
        >
        <v-btn
          variant="tonal"
          :href="`${repo}/issues`"
          target="_blank"
          rel="noreferrer"
          :prepend-icon="mdiForumOutline"
          >{{ zh ? "反馈问题" : "Report an issue" }}</v-btn
        >
      </div>
    </v-sheet>
  </div>
</template>

<style scoped>
.about {
  overflow-wrap: anywhere;
}
</style>
