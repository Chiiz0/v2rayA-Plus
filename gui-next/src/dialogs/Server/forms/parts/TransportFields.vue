<script setup lang="ts">
// The transport of a vmess or vless node: the network and the fields
// each network has (obfuscation for tcp/kcp/quic, host and path, the
// WebSocket early data, the gRPC and xhttp tuning, the QUIC security).
// Edits the v2ray model in place. Changing the network resets the
// obfuscation, and gRPC without TLS turns TLS on, as the old form did.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useNotify } from "@/composables/useNotify";
import type { V2rayModel } from "../../models";
import { validateXhttpExtra } from "@/lib/compatXhttp";

defineProps<{ readonly?: boolean }>();
const model = defineModel<V2rayModel>({ required: true });
const { t } = useI18n();
const notify = useNotify();

const networks = [
  { value: "tcp", title: "TCP" },
  { value: "kcp", title: "mKCP" },
  { value: "ws", title: "WebSocket" },
  { value: "h2", title: "HTTP/2" },
  { value: "grpc", title: "gRPC" },
  { value: "quic", title: "QUIC" },
  { value: "xhttp", title: "XHTTP" },
];
const tcpTypes = computed(() => [
  { value: "none", title: t("configureServer.noObfuscation") },
  { value: "http", title: t("configureServer.httpObfuscation") },
]);
const packetTypes = computed(() => [
  { value: "none", title: t("configureServer.noObfuscation") },
  { value: "srtp", title: t("configureServer.srtpObfuscation") },
  { value: "utp", title: t("configureServer.utpObfuscation") },
  { value: "wechat-video", title: t("configureServer.wechatVideoObfuscation") },
  {
    value: "dtls",
    title: `${t("configureServer.dtlsObfuscation")} (${t("configureServer.forceTLS")})`,
  },
  { value: "wireguard", title: t("configureServer.wireguardObfuscation") },
]);
const xhttpModes = ["auto", "packet-up", "stream-up", "stream-one"];
function validExtra(value: unknown): true | string {
  try {
    validateXhttpExtra(
      typeof value === "string" ? value : "",
      model.value.xhttpMode,
    );
    return true;
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

const net = computed(() => model.value.net);
const showsHost = computed(
  () =>
    ["ws", "h2", "xhttp"].includes(net.value) ||
    model.value.tls === "tls" ||
    model.value.tls === "reality" ||
    (net.value === "tcp" && model.value.type === "http"),
);
const showsPath = computed(
  () =>
    ["ws", "h2", "xhttp"].includes(net.value) ||
    (net.value === "tcp" && model.value.type === "http"),
);

function onNetwork() {
  model.value.type = "none";
  if (model.value.tls === "none" && net.value === "grpc") {
    notify.warning(t("setting.messages.grpcShouldWithTls"));
    model.value.tls = "tls";
  }
}
</script>

<template>
  <v-row density="compact">
    <v-col cols="12" sm="6">
      <v-select
        v-model="model.net"
        :items="networks"
        :label="t('configureServer.network')"
        :readonly="readonly"
        @update:model-value="onNetwork"
      />
    </v-col>
    <v-col v-if="net === 'tcp'" cols="12" sm="6">
      <v-select
        v-model="model.type"
        :items="tcpTypes"
        :label="t('configureServer.type')"
        :readonly="readonly"
      />
    </v-col>
    <v-col v-if="net === 'kcp' || net === 'quic'" cols="12" sm="6">
      <v-select
        v-model="model.type"
        :items="packetTypes"
        :label="t('configureServer.type')"
        :readonly="readonly"
      />
    </v-col>
    <v-col v-if="showsHost" cols="12" sm="6">
      <v-text-field
        v-model="model.host"
        :label="t('configureServer.hostObfuscation')"
        :readonly="readonly"
        dir="ltr"
      />
    </v-col>
    <v-col v-if="showsPath" cols="12" sm="6">
      <v-text-field
        v-model="model.path"
        :label="t('configureServer.pathObfuscation')"
        :readonly="readonly"
        dir="ltr"
      />
    </v-col>
    <template v-if="net === 'ws'">
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.maxEarlyData"
          type="number"
          :label="t('configureServer.maxEarlyData')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.earlyDataHeaderName"
          :label="t('configureServer.earlyDataHeaderName')"
          :readonly="readonly"
        />
      </v-col>
    </template>
    <v-col v-if="net === 'kcp'" cols="12" sm="6">
      <v-text-field
        v-model="model.path"
        :label="t('configureServer.seedObfuscation')"
        :readonly="readonly"
      />
    </v-col>
    <template v-if="net === 'grpc'">
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.path"
          :label="t('configureServer.serviceName')"
          :readonly="readonly"
          dir="ltr"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-switch
          v-model="model.multiMode"
          :label="t('configureServer.multiMode')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.idleTimeout"
          type="number"
          :label="t('configureServer.idleTimeout')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.healthCheckTimeout"
          type="number"
          :label="t('configureServer.healthCheckTimeout')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-switch
          v-model="model.permitWithoutStream"
          :label="t('configureServer.permitWithoutStream')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.initialWindowsSize"
          type="number"
          :label="t('configureServer.initialWindowsSize')"
          :readonly="readonly"
        />
      </v-col>
    </template>
    <template v-if="net === 'xhttp'">
      <v-col cols="12" sm="6">
        <v-select
          v-model="model.xhttpMode"
          :items="xhttpModes"
          :label="t('configureServer.mode')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12">
        <v-textarea
          v-model="model.xhttpExtraJson"
          label="XHTTP extra (JSON)"
          :rules="[validExtra]"
          :readonly="readonly"
          rows="8"
          auto-grow
          spellcheck="false"
          dir="ltr"
          placeholder="{}"
        />
      </v-col>
    </template>
    <template v-if="net === 'quic'">
      <v-col cols="12" sm="6">
        <v-select
          v-model="model.quicSecurity"
          :items="['none', 'aes-128-gcm', 'chacha20-poly1305']"
          :label="t('configureServer.quicSecurity')"
          :readonly="readonly"
        />
      </v-col>
      <v-col cols="12" sm="6">
        <v-text-field
          v-model="model.key"
          :label="t('configureServer.key')"
          :readonly="readonly"
        />
      </v-col>
    </template>
  </v-row>
</template>
