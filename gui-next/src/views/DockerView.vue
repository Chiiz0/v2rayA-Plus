<script setup lang="ts">
import { onMounted } from "vue";
import { useI18n } from "vue-i18n";
import {
  mdiPlus,
  mdiRefresh,
  mdiPlay,
  mdiPause,
  mdiPencil,
  mdiDelete,
  mdiSpeedometer,
} from "@mdi/js";
import { useDockerProxies } from "./docker/model";
const { t } = useI18n();
const model = useDockerProxies();
const { proxies, loading, loadError, busy, latencies, testing, sync } = model;
onMounted(sync);
defineExpose({ sync });
</script>

<template>
  <div>
    <div class="d-flex flex-wrap ga-2 align-center mb-4">
      <p class="text-body-2 text-medium-emphasis flex-grow-1 ma-0">
        {{ t("docker.description") }}
      </p>
      <v-btn
        variant="text"
        :prepend-icon="mdiRefresh"
        :loading="loading"
        :disabled="busy"
        @click="sync"
        >{{ t("docker.refresh") }}</v-btn
      >
      <v-btn
        variant="tonal"
        :prepend-icon="mdiSpeedometer"
        :loading="testing"
        :disabled="
          busy || loading || !proxies.some((p) => p.status === 'running')
        "
        @click="model.test()"
        >{{ t("docker.testAll") }}</v-btn
      >
      <v-btn
        color="primary"
        variant="flat"
        :prepend-icon="mdiPlus"
        :disabled="busy"
        @click="model.edit()"
        >{{ t("docker.create") }}</v-btn
      >
    </div>
    <v-alert v-if="loadError" type="error" variant="tonal" class="mb-4">{{
      t("docker.loadFailed", { message: loadError })
    }}</v-alert>
    <v-skeleton-loader v-if="loading && !proxies.length" type="card" />
    <v-sheet
      v-else-if="!proxies.length && !loadError"
      color="surface-container-low"
      rounded="xl"
      class="pa-8 text-center"
      >{{ t("docker.empty") }}</v-sheet
    >
    <div class="docker-grid">
      <v-card
        v-for="proxy in proxies"
        :key="proxy.port"
        rounded="xl"
        variant="flat"
        color="surface-container-low"
      >
        <v-card-title class="d-flex align-center flex-wrap ga-2 pt-5 px-5">
          <span class="font-mono">SOCKS5 :{{ proxy.port }}</span
          ><v-spacer />
          <v-chip
            size="small"
            :color="proxy.status === 'running' ? 'success' : undefined"
            >{{
              proxy.status === "running"
                ? t("docker.running")
                : proxy.status === "paused"
                  ? t("docker.paused")
                  : proxy.status
            }}</v-chip
          >
        </v-card-title>
        <v-card-text class="px-5">
          <dl class="docker-details">
            <dt>{{ t("docker.target") }}</dt>
            <dd>{{ proxy.serverName || t("docker.staleNode") }}</dd>
            <dt>{{ t("docker.front") }}</dt>
            <dd>
              {{
                proxy.frontWhich
                  ? proxy.frontServerName || t("docker.staleNode")
                  : t("docker.noFront")
              }}
            </dd>
            <dt>{{ t("docker.core") }}</dt>
            <dd>
              {{
                !proxy.core || proxy.core === "auto"
                  ? t("docker.auto")
                  : proxy.core === "v2fly"
                    ? "V2Fly"
                    : "Xray"
              }}
            </dd>
            <dt>{{ t("docker.container") }}</dt>
            <dd class="font-mono">{{ proxy.containerName }}</dd>
            <dt>{{ t("docker.latency") }}</dt>
            <dd>{{ latencies[proxy.port] || "—" }}</dd>
          </dl>
        </v-card-text>
        <v-card-actions class="px-4 pb-4 flex-wrap ga-1">
          <v-btn
            :prepend-icon="mdiPencil"
            :disabled="busy || loading"
            @click="model.edit(proxy)"
            >{{ t("docker.edit") }}</v-btn
          >
          <v-btn
            :prepend-icon="proxy.status === 'running' ? mdiPause : mdiPlay"
            :disabled="busy || loading"
            @click="
              model.action(
                proxy,
                proxy.status === 'running' ? 'pause' : 'resume',
              )
            "
            >{{
              t(proxy.status === "running" ? "docker.pause" : "docker.resume")
            }}</v-btn
          >
          <v-btn
            :prepend-icon="mdiSpeedometer"
            :loading="testing"
            :disabled="busy || loading || proxy.status !== 'running'"
            @click="model.test([proxy])"
            >{{ t("docker.httpTest") }}</v-btn
          >
          <v-btn
            color="error"
            :prepend-icon="mdiDelete"
            :disabled="busy || loading"
            @click="model.action(proxy, 'remove')"
            >{{ t("docker.remove") }}</v-btn
          >
        </v-card-actions>
      </v-card>
    </div>
  </div>
</template>
<style scoped>
.docker-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 360px), 1fr));
  gap: 16px;
}
.docker-details {
  display: grid;
  grid-template-columns: minmax(90px, auto) minmax(0, 1fr);
  gap: 8px 16px;
  margin-top: 12px;
}
.docker-details dt {
  opacity: 0.7;
}
.docker-details dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.font-mono {
  font-family: monospace;
}
</style>
