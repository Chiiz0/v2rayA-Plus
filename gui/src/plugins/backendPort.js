// import { parseURL, isIntranet } from "@/assets/js/utils";
console.log("backendPort");
let ba = localStorage.getItem("backendAddress");
if (ba == null) {
  // const u = parseURL(location.href);
  // if (u.host !== "localhost" && u.host !== "local" && isIntranet(u.host)) {
  //   localStorage["backendAddress"] = `${u.protocol}://${u.host}:2017`;
  // } else {
  //   localStorage.setItem("backendAddress", "http://localhost:2017");
  // }
  localStorage.setItem("backendAddress", "");
}

// The classic UI lives alongside the modern one. Both share the same API prefix.
const prefix = location.pathname.replace(/\/legacy\/?$/, "").replace(/\/$/, "");
if ((!ba || (ba.startsWith("/") && !ba.startsWith("//"))) && location.pathname.endsWith("/legacy/")) {
  localStorage.setItem("backendAddress", prefix);
}
