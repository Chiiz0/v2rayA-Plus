const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { createRequire } = require("node:module");
const gui = path.resolve(__dirname, "..");
const requireGui = createRequire(path.join(gui, "package.json"));
const babel = requireGui("@babel/core");
const { Base64 } = requireGui("js-base64");

// Execute the real component methods without rendering UI or making requests.
// Utilities and components share a realm because handleResponse checks instanceof Function.
const realm = vm.createContext({
  apiRoot: "/api",
  console,
  setTimeout,
  clearTimeout,
});
function load(source, imports) {
  const { code } = babel.transformSync(source, {
    configFile: false,
    babelrc: false,
    plugins: [requireGui.resolve("@babel/plugin-transform-modules-commonjs")],
  });
  const module = { exports: {} };
  // parseURL uses an anchor element to read URL fields; this is its only DOM dependency.
  const document = {
    createElement() {
      let url;
      return new Proxy(
        { remove() {} },
        {
          set(target, property, value) {
            if (property === "href") url = new URL(value);
            else target[property] = value;
            return true;
          },
          get(target, property) {
            return property in target ? target[property] : url[property];
          },
        }
      );
    },
  };
  realm.document = document;
  vm.runInContext(
    "(function(exports, require, module) {" + code + "\n})",
    realm
  )(
    module.exports,
    (name) =>
      imports[name] ||
      (name === "urijs" || name === "js-base64" ? requireGui(name) : {}),
    module
  );
  return module.exports;
}
const utils = load(
  fs.readFileSync(path.join(gui, "src/assets/js/utils.js"), "utf8"),
  { "./const.js": {} }
);
function component(filename) {
  return load(
    fs
      .readFileSync(path.join(gui, filename), "utf8")
      .match(/<script>([\s\S]*?)<\/script>/)[1],
    { "@/assets/js/utils": utils }
  ).default;
}
const editor = component("src/components/modalServer.vue");
const page = component("src/node.vue");
const link =
  "vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?type=raw&encryption=mlkem768x25519plus.native.0rtt.test&security=none";
function setup(which = { id: 1, _type: "server" }) {
  const c = {
    ...editor.data(),
    ...editor.methods,
    which,
    readonly: false,
    submitting: false,
    modalServerSession: 0,
    requests: [],
    notices: [],
    emitted: [],
    closed: 0,
    $refs: {
      section: {
        querySelectorAll() {
          return [];
        },
      },
    },
    $t(key, params) {
      return params ? key + ": " + params.message : key;
    },
    $nextTick(callback) {
      callback();
    },
    $axios(config) {
      return new Promise((resolve, reject) =>
        this.requests.push({ config, resolve, reject })
      );
    },
    $emit(...args) {
      this.emitted.push(args);
    },
  };
  Object.defineProperty(c, "canSubmit", {
    get: () => editor.computed.canSubmit.call(c),
  });
  c.$parent = {
    close() {
      c.closed++;
    },
  };
  c.$buefy = {
    toast: {
      open(value) {
        c.notices.push(value);
      },
    },
  };
  return c;
}
const good = { data: { code: "SUCCESS", data: { sharingAddress: link } } };
(async () => {
  for (const network of ["raw", "RAW", "tcp"]) {
    const encryption = "mlkem768x25519plus.native.0rtt.test+key/=";
    const params = new URLSearchParams({
      type: network,
      security: "none",
      headerType: "http",
      host: "example.test",
      path: "/transport",
      encryption,
      flow: "xtls-rprx-vision",
    });
    const model = editor.methods.resolveURL(
      "vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?" +
        params
    );
    assert.equal(model.net, "tcp");
    model.ps = "edited";
    const saved = new URL(editor.methods.generateURL(model));
    assert.equal(saved.searchParams.get("type"), "tcp");
    assert.equal(saved.searchParams.get("headerType"), "http");
    assert.equal(saved.searchParams.get("host"), "example.test");
    assert.equal(saved.searchParams.get("path"), "/transport");
    assert.equal(saved.searchParams.get("encryption"), encryption);
    assert.equal(saved.searchParams.get("flow"), "xtls-rprx-vision");
    assert.equal(saved.searchParams.has("scy"), false);
    assert.equal(saved.hash, "#edited");
    console.log(
      `PASS: VLESS ${network} edits preserve Encryption, Flow and transport parameters`
    );
  }
  for (const network of ["raw", "RAW", "tcp"]) {
    for (const numeric of [true, false]) {
      const fields = {
        port: numeric ? 24443 : "24443",
        aid: numeric ? 0 : "0",
        v: numeric ? 2 : "2",
      };
      const source = {
        ...fields,
        net: network,
        type: "http",
        add: "203.0.113.10",
        id: "11111111-1111-4111-8111-111111111111",
        ps: "before",
        host: "example.test",
        path: "/transport",
        scy: "aes-128-gcm",
      };
      const model = editor.methods.resolveURL(
        "vmess://" + Base64.encode(JSON.stringify(source))
      );
      assert.equal(model.net, "tcp");
      model.ps = "edited";
      const saved = JSON.parse(
        Base64.decode(editor.methods.generateURL(model).slice(8))
      );
      for (const [name, value] of Object.entries(fields))
        assert.equal(saved[name], value);
      assert.equal(saved.net, "tcp");
      assert.equal(saved.type, "http");
      assert.equal(saved.host, "example.test");
      assert.equal(saved.path, "/transport");
      assert.equal(saved.ps, "edited");
      assert.equal(saved.scy, "aes-128-gcm");
      assert.equal(saved.encryption, undefined);
      console.log(
        `PASS: VMess ${network} with ${
          numeric ? "numeric" : "string"
        } fields preserves port/aid/v and cipher`
      );
    }
  }
  {
    const c = setup();
    const pending = editor.mounted.call(c);
    assert.equal(c.serverLoading, true);
    assert.equal(c.serverLoaded, false);
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 0);
    c.requests[0].resolve(good);
    await pending;
    assert.equal(c.serverLoading, false);
    assert.equal(c.serverLoaded, true);
    assert.equal(c.closed, 0);
    assert.equal(c.v2ray.net, "tcp");
    assert.equal(c.v2ray.encryption, "mlkem768x25519plus.native.0rtt.test");
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 1);
    assert.equal(
      new URL(c.emitted[0][1]).searchParams.get("encryption"),
      c.v2ray.encryption
    );
    c.submitting = true;
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 1);
    c.submitting = false;
    c.readonly = true;
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 1);
    console.log(
      "PASS: pending/readonly/submitting editor cannot save; successful load preserves raw normalization and encryption"
    );
  }
  for (const kind of [
    "api",
    "network",
    "missing-body",
    "missing-link",
    "unsupported-link",
    "malformed-link",
  ]) {
    const c = setup();
    const pending = editor.mounted.call(c);
    switch (kind) {
      case "api":
        c.requests[0].resolve({
          data: { code: "ERROR", message: "not found" },
        });
        break;
      case "network":
        c.requests[0].reject(new Error("network unavailable"));
        break;
      case "missing-body":
        c.requests[0].resolve({});
        break;
      case "missing-link":
        c.requests[0].resolve({ data: { code: "SUCCESS", data: {} } });
        break;
      case "unsupported-link":
        c.requests[0].resolve({
          data: { code: "SUCCESS", data: { sharingAddress: "unknown://node" } },
        });
        break;
      case "malformed-link":
        c.requests[0].resolve({
          data: { code: "SUCCESS", data: { sharingAddress: "vmess://broken" } },
        });
        break;
    }
    await pending;
    assert.equal(c.serverLoaded, false);
    assert.equal(c.serverLoading, false);
    assert.equal(c.closed, 1);
    assert.equal(c.notices.length, 1);
    assert.match(c.notices[0].message, /^configureServer.loadFailed:/);
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 0);
    console.log(
      `PASS: ${kind} closes editor, reports failure and cannot overwrite with defaults`
    );
  }
  {
    const c = setup(null);
    await editor.mounted.call(c);
    assert.equal(c.serverLoaded, true);
    assert.equal(c.requests.length, 0);
    console.log(
      "PASS: create-new-node path does not require a sharing-address fetch"
    );
  }
  for (const failure of ["api", "network"]) {
    const c = setup();
    c.modalServerSubmitting = false;
    c.showModalServer = true;
    c.refreshes = 0;
    c.refreshTableData = () => c.refreshes++;
    c.updateConnectView = () => {};
    const pending = page.methods.handleModalServerSubmit.call(c, link);
    assert.equal(c.modalServerSubmitting, true);
    page.methods.handleModalServerSubmit.call(c, link);
    assert.equal(c.requests.length, 1);
    if (failure === "api")
      c.requests[0].resolve({
        data: { code: "ERROR", message: "save refused" },
      });
    else c.requests[0].reject(new Error("network unavailable"));
    await pending;
    assert.equal(c.modalServerSubmitting, false);
    assert.equal(c.showModalServer, true);
    assert.equal(c.refreshes, 0);
    assert.equal(c.notices.length, 1);
    const retry = page.methods.handleModalServerSubmit.call(c, link);
    assert.equal(c.requests.length, 2);
    c.requests[1].resolve({
      data: { code: "SUCCESS", data: { touch: {}, running: false } },
    });
    await retry;
    assert.equal(c.modalServerSubmitting, false);
    assert.equal(c.showModalServer, false);
    assert.equal(c.refreshes, 1);
    console.log(
      `PASS: duplicate saves blocked, ${failure} failure unlocks retry, successful retry closes and refreshes once`
    );
  }

  for (const outcome of ["success", "failure"]) {
    const old = setup();
    const pending = editor.mounted.call(old);
    editor.beforeDestroy.call(old);
    const reopened = setup({ id: 2, _type: "server" });
    old.$parent = reopened.$parent;
    if (outcome === "success") old.requests[0].resolve(good);
    else old.requests[0].reject(new Error("old load failed"));
    await pending;
    assert.equal(old.serverLoaded, false);
    assert.equal(old.v2ray.id, "");
    assert.equal(reopened.closed, 0);
    assert.equal(old.notices.length, 0);
    assert.equal(old.canSubmit, false);
    console.log(
      `PASS: old ${outcome} load after close/reopen cannot update or close the new editor`
    );
  }
  for (const state of ["destroyed", "submitting"]) {
    const c = setup(null);
    await editor.mounted.call(c);
    c.v2ray = c.resolveURL(link);
    c.v2ray.allowInsecure = true;
    let confirm;
    c.$buefy.dialog = {
      confirm: () =>
        new Promise((resolve) => {
          confirm = resolve;
        }),
    };
    const pending = c.handleClickSubmit();
    if (state === "destroyed") editor.beforeDestroy.call(c);
    else c.submitting = true;
    confirm({ result: true });
    await pending;
    assert.equal(c.emitted.length, 0);
    console.log(
      `PASS: confirmation resolved after ${state} state cannot submit`
    );
  }
  for (const outcome of ["success", "api-error", "network-error"]) {
    const c = setup();
    c.modalServerSubmitting = false;
    c.showModalServer = false;
    c.refreshes = 0;
    c.refreshTableData = () => c.refreshes++;
    c.updateConnectView = () => {};
    page.methods.handleClickModifyServer.call(c, { id: 1, _type: "server" });
    const previous = page.methods.handleModalServerSubmit.call(c, link);
    c.showModalServer = false;
    page.methods.handleClickModifyServer.call(c, { id: 2, _type: "server" });
    assert.equal(c.modalServerSubmitting, false);
    const current = page.methods.handleModalServerSubmit.call(c, link);
    assert.equal(c.modalServerSubmitting, true);
    assert.equal(c.requests[0].config.data.which.id, 1);
    assert.equal(c.requests[1].config.data.which.id, 2);
    if (outcome === "success")
      c.requests[0].resolve({
        data: { code: "SUCCESS", data: { touch: {}, running: false } },
      });
    else if (outcome === "api-error")
      c.requests[0].resolve({
        data: { code: "ERROR", message: "old save refused" },
      });
    else c.requests[0].reject(new Error("old save failed"));
    await previous;
    assert.equal(c.showModalServer, true);
    assert.equal(c.modalServerSubmitting, true);
    assert.equal(c.which.id, 2);
    assert.equal(c.notices.length, 0);
    assert.equal(c.refreshes, 0);
    c.requests[1].resolve({
      data: { code: "SUCCESS", data: { touch: {}, running: false } },
    });
    await current;
    assert.equal(c.showModalServer, false);
    assert.equal(c.modalServerSubmitting, false);
    assert.equal(c.refreshes, 1);
    console.log(
      `PASS: old ${outcome} save cannot close, refresh or unlock the new editor session`
    );
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
