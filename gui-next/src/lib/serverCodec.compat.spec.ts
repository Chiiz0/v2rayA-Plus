// @vitest-environment happy-dom
import { describe, expect, test } from "vitest";
import { Base64 } from "js-base64";
import { parseShareLink, generateShareLink } from "./serverCodec";
import { parseURL } from "./url";

const extra =
  '{"noGRPCHeader":false,"noSSEHeader":false,"scMaxBufferedPosts":0,"scMaxEachPostBytes":"1000-2000","scMinPostsIntervalMs":"-1--1","xPaddingBytes":0,"xmux":{"maxConcurrency":0,"maxConnections":"2-4","hKeepAlivePeriod":-1},"headers":{"X-Test":"a:b},\\\"+%=/"},"unknown":{"large":9007199254740993,"enabled":false,"values":[0,-1,null]},"downloadSettings":{"network":"xhttp","address":"download.example","port":443,"xhttpSettings":{"path":"/down","mode":"stream-up","extra":{"unknownNested":{"v":0},"noSSEHeader":false}}}}';
const vless = (raw = extra) =>
  `vless://id@example.com:443?type=xhttp&mode=packet-up&path=%2Fx&host=cdn.example&security=tls&extra=${encodeURIComponent(raw)}#original`;
const vmess = (raw = extra) =>
  "vmess://" +
  Base64.encode(
    '{"protocol":"vmess","net":"xhttp","add":"example.com","port":"443","id":"id","ps":"original","host":"cdn.example","path":"/x","xhttpMode":"packet-up","xhttpExtra":' +
      raw +
      "}",
  );

for (const [protocol, link] of Object.entries({
  vless: vless(),
  vmess: vmess(),
})) {
  describe(`${protocol} XHTTP backend compatibility`, () => {
    test("rename, save and reopen retain unknown fields, large integers, explicit false, zero and negative ranges", () => {
      const model = parseShareLink(link)!;
      expect(model.xhttpExtraJson).toBe(extra);
      expect(model.xhttpMode).toBe("packet-up");
      model.ps = "renamed";
      const saved = generateShareLink(model)!;
      const reopened = parseShareLink(saved)!;
      expect(reopened.xhttpExtraJson).toBe(extra);
      expect(reopened.ps).toBe("renamed");
      expect(reopened.path).toBe("/x");
      expect(reopened.host).toBe("cdn.example");
      expect(
        protocol === "vmess"
          ? Base64.decode(saved.slice(8))
          : parseURL(saved).params.extra,
      ).toContain("9007199254740993");
    });
    test("clearing JSON removes old canonical and flattened settings", () => {
      const model = parseShareLink(link)!;
      model.xhttpExtraJson = "";
      model.noGRPCHeader = true;
      model.xmuxHKeepAlive = "90";
      const reopened = parseShareLink(generateShareLink(model)!)!;
      expect(reopened.xhttpExtraJson).toBe("{}");
    });
    test("invalid advanced JSON prevents creating a save URI", () => {
      const model = parseShareLink(link)!;
      for (const invalid of [
        "[]",
        "{",
        '{"xPaddingBytes":-1}',
        '{"headers":{"Host":"bad"}}',
        '{"xmux":{"maxConnections":1,"maxConcurrency":2}}',
      ]) {
        model.xhttpExtraJson = invalid;
        expect(() => generateShareLink(model)).toThrow();
      }
    });
  });
}

test("legacy splitHTTP settings normalize without losing int64 or disabled flags", () => {
  const link =
    "vless://id@example.com:443?type=splithttp&xhttpMode=packet-up&noGRPCHeader=false&scMaxBufferedPosts=9223372036854775807&xmuxHKeepAlive=-1&scMinPostsIntervalFrom=-1&scMinPostsIntervalTo=-1";
  const model = parseShareLink(link)!;
  expect(model.net).toBe("xhttp");
  expect(model.xhttpExtraJson).toContain(
    '"scMaxBufferedPosts":9223372036854775807',
  );
  expect(model.xhttpExtraJson).toContain('"noGRPCHeader":false');
  expect(model.xhttpExtraJson).toContain('"hKeepAlivePeriod":-1');
  expect(parseShareLink(generateShareLink(model)!)!.xhttpExtraJson).toBe(
    model.xhttpExtraJson,
  );
});

test("canonical VMess raw extra takes precedence over stale aliases", () => {
  const raw =
    '{"protocol":"vmess","net":"splithttp","add":"example.com","port":"443","id":"id","ps":"n","host":"cdn","path":"/x","xhttpMode":"auto","noGRPCHeader":true,"xhttpExtra":{"noGRPCHeader":false,"large":9007199254740993}}';
  const model = parseShareLink("vmess://" + Base64.encode(raw))!;
  expect(model.xhttpExtraJson).toBe(
    '{"noGRPCHeader":false,"large":9007199254740993}',
  );
  expect(parseShareLink(generateShareLink(model)!)!.xhttpExtraJson).toBe(
    model.xhttpExtraJson,
  );
});

test("VLESS raw transport and encryption survive editing, and encryption can be cleared", () => {
  const model = parseShareLink(
    "vless://id@example.com:443?type=raw&encryption=mlkem768x25519plus.native.0rtt.test&security=none",
  )!;
  expect(model.net).toBe("tcp");
  model.ps = "renamed";
  expect(parseShareLink(generateShareLink(model)!)!.encryption).toBe(
    "mlkem768x25519plus.native.0rtt.test",
  );
  model.encryption = "";
  expect(parseURL(generateShareLink(model)!).params.encryption).toBe("none");
});

for (const link of [
  "vless://id@example.com:443?security=tls&allowInsecure=true",
  "trojan://pwd@example.com:443?allowInsecure=1",
  "juicity://id:pwd@example.com:443?allow_insecure=1",
  "tuic://id:pwd@example.com:443?allow_insecure=true",
]) {
  test(`${link.split(":")[0]} preserves and can disable insecure TLS`, () => {
    const model = parseShareLink(link)!;
    expect(model.allowInsecure).toBe(true);
    expect(parseShareLink(generateShareLink(model)!)!.allowInsecure).toBe(true);
    model.allowInsecure = false;
    expect(parseShareLink(generateShareLink(model)!)!.allowInsecure).toBe(
      false,
    );
  });
}

test("SSR supports IPv6, unicode, colon passwords and optional parameters", () => {
  const source = {
    protocol: "ssr",
    server: "2001:db8::1",
    port: "1443",
    method: "aes-128-cfb",
    proto: "auth_chain_a",
    protoParam: "123:token",
    obfs: "tls1.2_ticket_auth",
    obfsParam: "cdn.example",
    name: "节点 + 100%",
    password: "secret:with:=+/%",
  };
  expect(parseShareLink(generateShareLink(source)!)).toEqual(source);
  const simple =
    "ssr://" + Base64.encodeURI("example.com:443:origin:none:plain:cHdk");
  expect(parseShareLink(simple)).toMatchObject({
    name: "",
    password: "pwd",
    protoParam: "",
    obfsParam: "",
  });
});

test("HTTP proxy links reopen after save, including HTTPS", () => {
  for (const protocol of ["http", "https"]) {
    const source = `${protocol}-proxy://user:pwd@example.com:8443#proxy`;
    const model = parseShareLink(source)!;
    expect(model.protocol).toBe(protocol);
    expect(parseShareLink(generateShareLink(model)!)).toEqual(model);
  }
});

test("query values containing literal equals are not truncated", () => {
  expect(
    parseURL('vless://id@example.com:443?extra={"custom":"a=b=c"}').params
      .extra,
  ).toBe('{"custom":"a=b=c"}');
});
