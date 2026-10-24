import { dockerEnglish, dockerChinese } from "../locales/plus-docker.ts";

export function applyPlusLocale(key, messages) {
  messages.about.intro =
    key === "zh"
      ? "支持 V2Ray / Xray、透明代理和独立 Docker 代理的 Web 管理工具。"
      : "Web management for V2Ray / Xray, transparent proxy and independent Docker proxies.";
  messages.onboarding.groupBody =
    key === "zh"
      ? "在代理页面选择节点加入分组。多个成员按该组的负载均衡策略分配；只保留一个成员即可固定使用该节点。"
      : "Add nodes to a group on the Proxies page. Multiple members follow the group’s load-balancing policy; keep one member to route through that node alone.";
  messages.docker = key === "zh" ? dockerChinese : dockerEnglish;
  messages.plus =
    key === "zh"
      ? { docker: "Docker 代理", legacy: "经典界面" }
      : { docker: "Docker proxies", legacy: "Classic interface" };
  messages.configureServer.allowInsecure =
    key === "zh" ? "允许不安全的 TLS" : "Allow insecure TLS";
}
