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
  URLSearchParams,
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
const xhttp = load(
  fs.readFileSync(path.join(gui, "src/assets/js/xhttp.js"), "utf8"),
  {}
);
function component(filename) {
  return load(
    fs
      .readFileSync(path.join(gui, filename), "utf8")
      .match(/<script>([\s\S]*?)<\/script>/)[1],
    { "@/assets/js/utils": utils, "@/assets/js/xhttp": xhttp }
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
  for (const name of ["xhttpError", "canSubmit"])
    Object.defineProperty(c, name, {
      get: () => editor.computed[name].call(c),
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
const xhttpLink = (params = {}) =>
  "vless://11111111-1111-4111-8111-111111111111@203.0.113.10:24443?" +
  new URLSearchParams({ type: "xhttp", security: "none", ...params });
const vmessXhttpLink = (extra, fields = {}) => {
  const payload = JSON.stringify({
    ps: "before",
    add: "203.0.113.10",
    port: 24443,
    id: "11111111-1111-4111-8111-111111111111",
    aid: 0,
    net: "xhttp",
    path: "/up",
    host: "upload.example",
    scy: "aes-128-gcm",
    xhttpMode: "packet-up",
    ...fields,
  });
  return (
    "vmess://" +
    Base64.encode(
      extra === undefined
        ? payload
        : payload.slice(0, -1) + ',"xhttpExtra":' + extra + "}"
    )
  );
};
(async () => {
  const extra =
    '{"noGRPCHeader":false,"noSSEHeader":false,"scMaxBufferedPosts":0,"scMaxEachPostBytes":"1000-2000","scMinPostsIntervalMs":"-1--1","xPaddingBytes":0,"xmux":{"maxConcurrency":0,"maxConnections":"2-4","hKeepAlivePeriod":-1},"headers":{"X-Test":"a:b},\\"+%=/"},"unknown":{"large":9007199254740993,"enabled":false,"values":[0,-1,null]},"downloadSettings":{"network":"xhttp","address":"download.example","port":443,"xhttpSettings":{"path":"/down","mode":"stream-up","extra":{"unknownNested":{"v":0},"noSSEHeader":false}}}}';
  for (const protocol of ["vless", "vmess"]) {
    const original =
      protocol === "vless"
        ? xhttpLink({
            mode: "packet-up",
            path: "/up",
            host: "upload.example",
            encryption: "mlkem768x25519plus.native.0rtt.test+key/=",
            extra,
          })
        : vmessXhttpLink(extra);
    const c = setup();
    const pending = editor.mounted.call(c);
    c.requests[0].resolve({
      data: { code: "SUCCESS", data: { sharingAddress: original } },
    });
    await pending;
    assert.equal(c.serverLoaded, true);
    assert.equal(c.canSubmit, true);
    assert.match(c.v2ray.xhttpExtraJson, /9007199254740993/);
    assert.deepEqual(JSON.parse(c.v2ray.xhttpExtraJson), JSON.parse(extra));
    c.v2ray.ps = "renamed";
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 1);
    const generated = c.emitted[0][1];
    const again = c.resolveURL(generated);
    assert.equal(again.ps, "renamed");
    assert.equal(again.path, "/up");
    assert.equal(again.host, "upload.example");
    assert.equal(again.xhttpMode, "packet-up");
    assert.deepEqual(JSON.parse(again.xhttpExtraJson), JSON.parse(extra));
    assert.match(again.xhttpExtraJson, /9007199254740993/);
    if (protocol === "vless") {
      const query = new URL(generated).searchParams;
      assert.equal(query.get("mode"), "packet-up");
      assert.equal(query.has("xhttpMode"), false);
      assert.equal(query.has("xhttpRawJson"), false);
      assert.equal(query.get("encryption"), c.v2ray.encryption);
    } else {
      const json = Base64.decode(generated.slice(8));
      assert.match(json, /9007199254740993/);
      const payload = JSON.parse(json);
      assert.equal(typeof payload.xhttpExtra, "object");
      assert.equal(payload.xhttpExtraJson, undefined);
      assert.equal(payload.xhttpRawJson, undefined);
      assert.equal(payload.scy, "aes-128-gcm");
      assert.equal(payload.encryption, undefined);
    }
    c.v2ray.xhttpExtraJson = "";
    const cleared = c.resolveURL(c.generateURL(c.v2ray));
    assert.equal(cleared.xhttpExtraJson, "{}");
    assert.equal(cleared.path, "/up");
    console.log(
      `PASS: ${protocol} XHTTP load/edit/save preserves extra, large integers, downloadSettings and explicit clearing`
    );
  }
  {
    const legacy = JSON.stringify({
      host: "legacy.example",
      path: "/legacy",
      mode: "stream-up",
      unknown: { keep: 0 },
      noSSEHeader: true,
      extra: { noSSEHeader: false },
    });
    for (const source of [
      xhttpLink({ xhttpRawJson: legacy }),
      vmessXhttpLink(undefined, {
        xhttpRawJson: legacy,
        host: "",
        path: "",
        xhttpMode: "",
      }),
      vmessXhttpLink(undefined, {
        xhttpRawJson: legacy,
        host: "",
        path: "",
        xhttpMode: "auto",
      }),
    ]) {
      const model = editor.methods.resolveURL(source);
      assert.equal(model.host, "legacy.example");
      assert.equal(model.path, "/legacy");
      assert.equal(model.xhttpMode, "stream-up");
      assert.deepEqual(JSON.parse(model.xhttpExtraJson), {
        unknown: { keep: 0 },
        noSSEHeader: false,
      });
      const saved = editor.methods.resolveURL(
        editor.methods.generateURL(model)
      );
      assert.equal(saved.path, "/legacy");
      assert.equal(saved.xhttpMode, "stream-up");
      assert.deepEqual(
        JSON.parse(saved.xhttpExtraJson),
        JSON.parse(model.xhttpExtraJson)
      );
    }
    const model = editor.methods.resolveURL(
      xhttpLink({
        host: "",
        path: "",
        mode: "auto",
        xhttpMode: "packet-up",
        xhttpRawJson: legacy,
        extra: "{}",
        noSSEHeader: "false",
        scMaxBufferedPosts: "0",
        xmuxHKeepAlive: "-1",
        xPaddingBytesFrom: "8",
        x_padding_bytes: "100-200",
      })
    );
    assert.equal(model.host, "");
    assert.equal(model.path, "");
    assert.equal(model.xhttpMode, "auto");
    assert.deepEqual(JSON.parse(model.xhttpExtraJson), {
      noSSEHeader: false,
      scMaxBufferedPosts: 0,
      xmux: { hKeepAlivePeriod: -1 },
      xPaddingBytes: 8,
    });
    const canonical = editor.methods.resolveURL(
      vmessXhttpLink("{}", {
        xhttpRawJson: "invalid legacy",
        noSSEHeader: true,
      })
    );
    assert.equal(canonical.xhttpExtraJson, "{}");
    for (const source of [
      xhttpLink({ xhttpRawJson: legacy, mode: "" }),
      xhttpLink({ xhttpRawJson: legacy, xhttpMode: "" }),
      vmessXhttpLink(undefined, {
        xhttpRawJson: legacy,
        mode: "",
        xhttpMode: "",
      }),
      vmessXhttpLink("{}", { xhttpRawJson: legacy, xhttpMode: "" }),
      vmessXhttpLink(undefined, {
        xhttpRawJson: legacy,
        mode: "auto",
        xhttpMode: "auto",
      }),
      vmessXhttpLink("{}", { xhttpRawJson: legacy, xhttpMode: "auto" }),
    ])
      assert.equal(editor.methods.resolveURL(source).xhttpMode, "auto");
    console.log(
      "PASS: canonical/legacy/flattened precedence preserves explicit empty basics, false, zero and one-sided ranges"
    );
  }
  {
    for (const alias of ["XHTTP", "splitHTTP", "SPLITHTTP"]) {
      for (const source of [
        xhttpLink({ type: alias }),
        vmessXhttpLink("{}", { net: alias }),
      ]) {
        const model = editor.methods.resolveURL(source);
        assert.equal(model.net, "xhttp");
        assert.equal(
          editor.methods.resolveURL(editor.methods.generateURL(model)).net,
          "xhttp"
        );
      }
    }
    const model = editor.methods.resolveURL(
      xhttpLink({
        type: "splithttp",
        extra:
          '{"scMaxBufferedPosts":9223372036854775807,"scMaxEachPostBytes":"2000-1000","xmux":{"hKeepAlivePeriod":9223372036854775807,"cMaxReuseTimes":"-2--1"}}',
      })
    );
    assert.equal(model.net, "xhttp");
    assert.match(model.xhttpExtraJson, /9223372036854775807/);
    assert.match(model.xhttpExtraJson, /2000-1000/);
    assert.match(editor.methods.generateURL(model), /type=xhttp/);
    console.log(
      "PASS: splitHTTP alias, reversed ranges and int64 values remain lossless"
    );
  }
  {
    const fields = {
      xhttpMode: "stream-up",
      extra: '{"future":{"big":9007199254740993}}',
      noGRPCHeader: false,
      noSSEHeader: "0",
      xPaddingObfsMode: "false",
      scMaxBufferedPosts: "9223372036854775807",
      serverMaxHeaderBytes: 0,
      scMaxEachPostBytesFrom: 0,
      scMaxEachPostBytesTo: 0,
      scMinPostsIntervalFrom: -1,
      scMinPostsIntervalTo: -2,
      scStreamUpServerFrom: 1,
      scStreamUpServerTo: 2,
      xPaddingBytesFrom: 0,
      xPaddingBytesTo: 0,
      xmuxMaxConcurFrom: 0,
      xmuxMaxConnFrom: 1,
      xmuxMaxConnTo: 2,
      xmuxCMaxReuseFrom: -1,
      xmuxHMaxReqFrom: 4,
      xmuxHMaxReqTo: 8,
      xmuxHMaxReusableTo: 30,
      xmuxHKeepAlive: "-9223372036854775808",
      xhttpHeaders: '{"X-Imported":"value"}',
      xPaddingKey: "",
      sessionIDLength: "4-8",
      uplinkChunkSize: "1024-2048",
      uplinkHTTPMethod: "PATCH",
    };
    for (const source of [
      xhttpLink(fields),
      vmessXhttpLink(undefined, fields),
    ]) {
      const model = editor.methods.resolveURL(source);
      const saved = editor.methods.resolveURL(
        editor.methods.generateURL(model)
      );
      assert.equal(saved.xhttpMode, "stream-up");
      assert.match(saved.xhttpExtraJson, /9007199254740993/);
      assert.match(saved.xhttpExtraJson, /9223372036854775807/);
      assert.match(saved.xhttpExtraJson, /-9223372036854775808/);
      const parsed = JSON.parse(saved.xhttpExtraJson);
      assert.equal(parsed.noGRPCHeader, false);
      assert.equal(parsed.noSSEHeader, false);
      assert.equal(parsed.xPaddingObfsMode, false);
      assert.equal(parsed.serverMaxHeaderBytes, 0);
      assert.equal(parsed.scMaxEachPostBytes, 0);
      assert.equal(parsed.scMinPostsIntervalMs, "-1--2");
      assert.equal(parsed.scStreamUpServerSecs, "1-2");
      assert.equal(parsed.xPaddingBytes, 0);
      assert.equal(parsed.xmux.maxConcurrency, 0);
      assert.equal(parsed.xmux.maxConnections, "1-2");
      assert.equal(parsed.xmux.cMaxReuseTimes, -1);
      assert.equal(parsed.xmux.hMaxRequestTimes, "4-8");
      assert.equal(parsed.xmux.hMaxReusableSecs, 30);
      assert.equal(parsed.headers["X-Imported"], "value");
      assert.equal(parsed.xPaddingKey, "");
      assert.equal(parsed.sessionIDLength, "4-8");
      assert.equal(parsed.uplinkChunkSize, "1024-2048");
      assert.equal(parsed.uplinkHTTPMethod, "PATCH");
    }
    console.log(
      "PASS: every legacy XHTTP flattened range and scalar maps losslessly in VMess and VLESS"
    );
  }
  const invalidExtras = [
    "{",
    "[]",
    "null",
    '"text"',
    '{"scMaxEachPostBytes":"0-100"}',
    '{"scMaxEachPostBytes":-1}',
    '{"scMaxEachPostBytes":1e3}',
    '{"xPaddingBytes":-1}',
    '{"xPaddingBytes":{"from":1,"to":2}}',
    '{"xPaddingBytes":2147483648}',
    '{"scMaxBufferedPosts":-1}',
    '{"scMaxBufferedPosts":9223372036854775808}',
    '{"serverMaxHeaderBytes":2147483648}',
    '{"noSSEHeader":"false"}',
    '{"uplinkHTTPMethod":null}',
    '{"sessionIDTable":"abc"}',
    '{"sessionIDTable":"abc","sessionIDLength":0}',
    '{"sessionIDTable":"abc","sessionIDLength":"-1-10"}',
    '{"sessionIDTable":"abc","sessionIDLength":"1-2147483647"}',
    '{"sessionIDTable":"abc","sessionIDLength":"2147483647-1"}',
    '{"headers":{"Host":"forbidden.example"}}',
    '{"headers":{"X-Test":0}}',
    '{"xmux":{"maxConcurrency":2,"maxConnections":3}}',
    '{"xmux":{"hKeepAlivePeriod":9223372036854775808}}',
    '{"downloadSettings":[]}',
    '{"downloadSettings":{"network":"tcp"}}',
    '{"downloadSettings":{"network":"xhttp","port":443}}',
    '{"downloadSettings":{"network":"xhttp","address":"","port":443}}',
    '{"downloadSettings":{"network":"xhttp","address":null,"port":443}}',
    '{"downloadSettings":{"network":"xhttp","address":"download.example"}}',
    '{"downloadSettings":{"network":"xhttp","address":"download.example","port":0}}',
    '{"downloadSettings":{"network":"xhttp","address":"download.example","port":65536}}',
    '{"downloadSettings":{"network":"xhttp","address":"download.example","port":"443"}}',
    '{"downloadSettings":{"network":"xhttp","address":"download.example","port":443,"xhttpSettings":{"extra":{"scMaxEachPostBytes":-1}}}}',
  ];
  for (const bad of invalidExtras) {
    const c = setup(null);
    await editor.mounted.call(c);
    c.v2ray = c.resolveURL(xhttpLink());
    c.v2ray.xhttpExtraJson = bad;
    assert.match(c.xhttpError, /^configureServer.xhttpInvalidExtra:/);
    assert.equal(c.canSubmit, false);
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 0);
    assert.throws(() => c.generateURL(c.v2ray));
  }
  for (const valid of [
    '{"sessionIDTable":"abc","sessionIDLength":"12-8"}',
    '{"sessionIDTable":"abc","sessionIDLength":2147483646}',
    '{"sessionIDTable":"","sessionIDLength":0}',
    '{"sessionIDTable":""}',
  ]) {
    assert.doesNotThrow(() => xhttp.validateXhttpExtra(valid));
  }
  {
    const c = setup(null);
    await editor.mounted.call(c);
    c.v2ray = c.resolveURL(xhttpLink({ extra }));
    c.v2ray.xhttpMode = "stream-one";
    assert.match(c.xhttpError, /downloadSettings/);
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 0);
    c.v2ray.xhttpMode = "auto";
    assert.equal(c.canSubmit, true);
    console.log(
      `PASS: ${invalidExtras.length} invalid extra shapes/ranges and incompatible download mode cannot save; correcting input unlocks save`
    );
  }
  {
    const c = setup();
    const pending = editor.mounted.call(c);
    c.requests[0].resolve({
      data: {
        code: "SUCCESS",
        data: { sharingAddress: xhttpLink({ extra: "[]" }) },
      },
    });
    await pending;
    assert.equal(c.closed, 1);
    assert.equal(c.serverLoaded, false);
    await c.handleClickSubmit();
    assert.equal(c.emitted.length, 0);
    const edited = setup(null);
    await editor.mounted.call(edited);
    edited.v2ray = edited.resolveURL(xhttpLink());
    edited.v2ray.allowInsecure = true;
    let confirm;
    edited.$buefy.dialog = {
      confirm: () =>
        new Promise((resolve) => {
          confirm = resolve;
        }),
    };
    const submitting = edited.handleClickSubmit();
    edited.v2ray.xhttpExtraJson = "[]";
    confirm({ result: true });
    await submitting;
    assert.equal(edited.emitted.length, 0);
    console.log(
      "PASS: invalid XHTTP on load or while confirmation is pending cannot overwrite a node"
    );
  }
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
