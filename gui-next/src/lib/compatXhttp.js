// Keep XHTTP's extra object as JSON text in the editor. Unknown fields (including
// integers outside JavaScript's safe range) must survive an unrelated node edit.
const has = (obj, key) => Object.prototype.hasOwnProperty.call(obj, key);
const rangeFields = [
  ["scMaxEachPostBytes", "scMaxEachPostBytes"],
  ["scMinPostsInterval", "scMinPostsIntervalMs"],
  ["scStreamUpServer", "scStreamUpServerSecs"],
  ["xPaddingBytes", "xPaddingBytes"],
  ["xmuxMaxConcur", "maxConcurrency", "xmux"],
  ["xmuxMaxConn", "maxConnections", "xmux"],
  ["xmuxCMaxReuse", "cMaxReuseTimes", "xmux"],
  ["xmuxHMaxReq", "hMaxRequestTimes", "xmux"],
  ["xmuxHMaxReusable", "hMaxReusableSecs", "xmux"],
];
const booleanFields = ["noGRPCHeader", "noSSEHeader", "xPaddingObfsMode"];
const stringFields = [
  "uplinkHTTPMethod",
  "xPaddingKey",
  "xPaddingHeader",
  "xPaddingPlacement",
  "xPaddingMethod",
  "sessionPlacement",
  "sessionKey",
  "sessionIDPlacement",
  "sessionIDKey",
  "sessionIDTable",
  "seqPlacement",
  "seqKey",
  "uplinkDataPlacement",
  "uplinkDataKey",
];
const directRanges = [
  "scMaxEachPostBytes",
  "scMinPostsIntervalMs",
  "scStreamUpServerSecs",
  "xPaddingBytes",
  "sessionIDLength",
  "uplinkChunkSize",
];
const integerFields = ["scMaxBufferedPosts", "serverMaxHeaderBytes"];
const modes = ["auto", "packet-up", "stream-up", "stream-one"];

function invalid(field, reason) {
  throw new Error(`${field}: ${reason}`);
}

// JSON.parse validates the document first. This small scanner only finds the
// boundaries of its already-valid values, so merging legacy fields never needs
// to parse and reserialize an unknown value (or round a large JSON integer).
function valueEnd(text, start) {
  let depth = 0;
  let quoted = false;
  for (let i = start; i < text.length; i++) {
    const char = text[i];
    if (quoted) {
      if (char === "\\") i++;
      else if (char === '"') {
        quoted = false;
        if (depth === 0) return i + 1;
      }
    } else if (char === '"') quoted = true;
    else if (char === "{" || char === "[") depth++;
    else if (char === "}" || char === "]") {
      if (depth === 0) return i;
      if (--depth === 0) return i + 1;
    } else if (char === "," && depth === 0) return i;
  }
  return text.length;
}

function objectFields(text, field) {
  if (typeof text !== "string") invalid(field, "expected a JSON object");
  let parsed;
  try {
    parsed = JSON.parse(text);
  } catch {
    invalid(field, "expected a valid JSON object");
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed))
    invalid(field, "expected a JSON object");
  const fields = new Map();
  let cursor = text.indexOf("{") + 1;
  while (cursor < text.length) {
    while (/\s/.test(text[cursor])) cursor++;
    if (text[cursor] === "}") break;
    const keyEnd = valueEnd(text, cursor);
    const key = JSON.parse(text.slice(cursor, keyEnd));
    cursor = text.indexOf(":", keyEnd) + 1;
    while (/\s/.test(text[cursor])) cursor++;
    const end = valueEnd(text, cursor);
    fields.set(key, text.slice(cursor, end).trim());
    cursor = end;
    while (/\s/.test(text[cursor])) cursor++;
    if (text[cursor] === ",") cursor++;
  }
  return fields;
}

function objectJSON(fields) {
  return `{${[...fields]
    .map(([k, v]) => `${JSON.stringify(k)}:${v}`)
    .join(",")}}`;
}

function integer(value, field) {
  if (
    (typeof value !== "number" && typeof value !== "string") ||
    !/^[+-]?\d+$/.test(String(value)) ||
    !Number.isSafeInteger(Number(value))
  )
    invalid(field, "expected an integer");
  return Number(value);
}

function range(value, field) {
  let ends;
  if (typeof value === "number") ends = [value, value];
  else if (typeof value === "string") {
    const match = /^([+-]?\d+)(?:-([+-]?\d+))?$/.exec(value);
    if (match) ends = [Number(match[1]), Number(match[2] ?? match[1])];
  }
  if (
    !ends ||
    ends.some((n) => !Number.isInteger(n) || n < -2147483648 || n > 2147483647)
  )
    invalid(field, 'expected an int32 or an "a-b" range');
  return ends;
}

function jsonRange(raw, field) {
  const value = JSON.parse(raw);
  if (typeof value === "number" && !/^-?\d+$/.test(raw))
    invalid(field, 'expected an int32 or an "a-b" range');
  return range(value, field);
}

function int64(value, field) {
  const text = String(value);
  if (!/^[+-]?\d+$/.test(text)) invalid(field, "expected an int64");
  const negative = text.startsWith("-");
  const digits = text.replace(/^[+-]/, "").replace(/^0+(?=\d)/, "");
  const maximum = negative ? "9223372036854775808" : "9223372036854775807";
  if (
    digits.length > maximum.length ||
    (digits.length === maximum.length && digits > maximum)
  )
    invalid(field, "expected an int64");
  return negative && digits !== "0" ? `-${digits}` : digits;
}

function validateExtra(fields, mode, depth = 0) {
  if (depth > 16) invalid("downloadSettings", "nesting is too deep");
  if (mode !== "" && !modes.includes(mode))
    invalid("mode", "expected auto, packet-up, stream-up or stream-one");
  for (const [key, parent] of [
    ...directRanges.map((key) => [key]),
    ...rangeFields
      .filter(([, , parent]) => parent)
      .map(([, key, parent]) => [key, parent]),
  ]) {
    const container = parent
      ? fields.has(parent)
        ? objectFields(fields.get(parent), parent)
        : new Map()
      : fields;
    if (!container.has(key)) continue;
    const label = parent ? `${parent}.${key}` : key;
    const ends = jsonRange(container.get(key), label);
    if (
      key === "scMaxEachPostBytes" &&
      !(ends[0] === 0 && ends[1] === 0) &&
      ends.some((n) => n <= 0)
    )
      invalid(
        label,
        "both endpoints must be positive, or both 0 for the core default",
      );
    if (key === "xPaddingBytes" && ends.some((n) => n < 0))
      invalid(label, "endpoints cannot be negative");
  }
  for (const key of booleanFields) {
    if (fields.has(key) && typeof JSON.parse(fields.get(key)) !== "boolean")
      invalid(key, "expected true or false");
  }
  for (const key of stringFields) {
    if (fields.has(key) && typeof JSON.parse(fields.get(key)) !== "string")
      invalid(key, "expected a string");
  }
  if (
    fields.has("sessionIDTable") &&
    JSON.parse(fields.get("sessionIDTable")) !== ""
  ) {
    if (!fields.has("sessionIDLength"))
      invalid("sessionIDLength", "required when sessionIDTable is not empty");
    if (
      jsonRange(fields.get("sessionIDLength"), "sessionIDLength").some(
        (n) => n <= 0 || n === 2147483647,
      )
    )
      invalid(
        "sessionIDLength",
        "endpoints must be from 1 to 2147483646 when sessionIDTable is not empty",
      );
  }
  for (const key of integerFields) {
    if (!fields.has(key)) continue;
    const raw = fields.get(key);
    if (key === "scMaxBufferedPosts") {
      if (int64(raw, key).startsWith("-"))
        invalid(key, "expected a non-negative int64");
      continue;
    }
    if (!/^-?\d+$/.test(raw) || Number(raw) < 0 || Number(raw) > 2147483647)
      invalid(key, "expected a non-negative int32");
  }
  if (fields.has("headers")) {
    for (const [key, value] of objectFields(fields.get("headers"), "headers")) {
      if (key.toLowerCase() === "host")
        invalid("headers", "use the Host field instead of a Host header");
      if (typeof JSON.parse(value) !== "string")
        invalid("headers", "every header value must be a string");
    }
  }
  if (fields.has("xmux")) {
    const xmux = objectFields(fields.get("xmux"), "xmux");
    if (xmux.has("hKeepAlivePeriod")) {
      int64(xmux.get("hKeepAlivePeriod"), "xmux.hKeepAlivePeriod");
    }
    const positive = (key) =>
      xmux.has(key) &&
      jsonRange(xmux.get(key), `xmux.${key}`).some((n) => n > 0);
    if (positive("maxConcurrency") && positive("maxConnections"))
      invalid(
        "xmux",
        "maxConcurrency and maxConnections cannot both be positive",
      );
  }
  if (fields.has("downloadSettings")) {
    if (mode === "stream-one")
      invalid("downloadSettings", "cannot be used with stream-one");
    const download = objectFields(
      fields.get("downloadSettings"),
      "downloadSettings",
    );
    const network = download.get(download.has("method") ? "method" : "network");
    if (!network || !["xhttp", "splithttp"].includes(JSON.parse(network)))
      invalid("downloadSettings.network", "expected xhttp or splithttp");
    const address = download.has("address")
      ? JSON.parse(download.get("address"))
      : null;
    if (typeof address !== "string" || address.trim() === "")
      invalid("downloadSettings.address", "expected a non-empty address");
    const port = download.get("port") || "";
    if (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535)
      invalid("downloadSettings.port", "expected an integer from 1 to 65535");
    const settings =
      download.get("xhttpSettings") ?? download.get("splithttpSettings");
    if (settings !== undefined) {
      let nested = objectFields(settings, "downloadSettings.xhttpSettings");
      const nestedMode = nested.has("mode")
        ? JSON.parse(nested.get("mode"))
        : "auto";
      if (nested.has("extra"))
        nested = objectFields(
          nested.get("extra"),
          "downloadSettings.xhttpSettings.extra",
        );
      validateExtra(nested, nestedMode, depth + 1);
    }
  }
}

export function validateXhttpExtra(text = "", mode = "auto") {
  if (typeof text !== "string") invalid("extra", "expected a JSON object");
  const fields = objectFields(text.trim() || "{}", "extra");
  validateExtra(fields, mode);
  return objectJSON(fields);
}

export function readXhttpSettings(params, vmessJSON) {
  const vmess = vmessJSON ? objectFields(vmessJSON, "VMess") : new Map();
  const scalar = (key) =>
    vmess.has(key) && typeof params[key] !== "string"
      ? vmess.get(key)
      : params[key];
  // Canonical VMess storage wins over legacy aliases left in an older payload.
  if (vmess.has("xhttpExtra")) {
    const mode = params.xhttpMode || "auto";
    for (const key of ["host", "path", "xhttpMode"])
      if (has(params, key) && typeof params[key] !== "string")
        invalid(key, "expected a string");
    return {
      host: params.host || "",
      path: params.path || "",
      xhttpMode: mode,
      xhttpExtraJson: validateXhttpExtra(vmess.get("xhttpExtra"), mode),
    };
  }
  const raw =
    has(params, "xhttpRawJson") && params.xhttpRawJson !== ""
      ? objectFields(
          typeof params.xhttpRawJson === "string"
            ? params.xhttpRawJson
            : vmess.get("xhttpRawJson"),
          "xhttpRawJson",
        )
      : new Map();
  for (const key of ["host", "path", "mode"])
    if (raw.has(key) && typeof JSON.parse(raw.get(key)) !== "string")
      invalid(`xhttpRawJson.${key}`, "expected a string");
  let extra = new Map(raw);
  for (const key of ["host", "path", "mode", "extra"]) extra.delete(key);
  if (raw.has("extra"))
    for (const [key, value] of objectFields(
      raw.get("extra"),
      "xhttpRawJson.extra",
    ))
      extra.set(key, value);
  if (has(params, "extra"))
    extra = objectFields(
      typeof params.extra === "string" ? params.extra : vmess.get("extra"),
      "extra",
    );
  const set = (key, value, parent) => {
    if (!parent) extra.set(key, value);
    else {
      const nested = extra.has(parent)
        ? objectFields(extra.get(parent), parent)
        : new Map();
      nested.set(key, value);
      extra.set(parent, objectJSON(nested));
    }
  };
  for (const key of directRanges) {
    if (has(params, key)) set(key, JSON.stringify(scalar(key)));
  }
  for (const [prefix, key, parent] of rangeFields) {
    const from = `${prefix}From`,
      to = `${prefix}To`;
    if (!has(params, from) && !has(params, to)) continue;
    const a = integer(has(params, from) ? scalar(from) : scalar(to), from);
    const b = integer(has(params, to) ? scalar(to) : scalar(from), to);
    set(key, JSON.stringify(a === b ? a : `${a}-${b}`), parent);
  }
  for (const key of booleanFields) {
    if (!has(params, key)) continue;
    const value = String(params[key]);
    if (!/^(true|false|t|f|1|0)$/i.test(value))
      invalid(key, "expected true or false");
    set(key, /^(true|t|1)$/i.test(value) ? "true" : "false");
  }
  for (const key of stringFields) {
    if (has(params, key)) {
      if (typeof params[key] !== "string") invalid(key, "expected a string");
      set(key, JSON.stringify(params[key]));
    }
  }
  for (const key of integerFields) {
    if (!has(params, key)) continue;
    const value =
      vmess.has(key) && typeof params[key] === "number"
        ? vmess.get(key)
        : params[key];
    set(
      key,
      key === "scMaxBufferedPosts"
        ? int64(value, key)
        : String(integer(value, key)),
    );
  }
  if (has(params, "xmuxHKeepAlive"))
    set(
      "hKeepAlivePeriod",
      int64(
        vmess.has("xmuxHKeepAlive") && typeof params.xmuxHKeepAlive === "number"
          ? vmess.get("xmuxHKeepAlive")
          : params.xmuxHKeepAlive,
        "xmuxHKeepAlive",
      ),
      "xmux",
    );
  if (has(params, "xhttpHeaders"))
    set(
      "headers",
      objectJSON(
        objectFields(
          typeof params.xhttpHeaders === "string"
            ? params.xhttpHeaders
            : vmess.get("xhttpHeaders"),
          "xhttpHeaders",
        ),
      ),
    );
  if (!extra.has("xPaddingBytes") && has(params, "x_padding_bytes")) {
    range(params.x_padding_bytes, "x_padding_bytes");
    set("xPaddingBytes", JSON.stringify(params.x_padding_bytes));
  }
  const basic = (key, fallback) => {
    const explicit =
      has(params, key) && !(vmessJSON && raw.size && params[key] === "");
    const value = explicit
      ? params[key]
      : raw.has(key)
        ? JSON.parse(raw.get(key))
        : fallback;
    if (typeof value !== "string") invalid(key, "expected a string");
    return value;
  };
  const selectedMode = has(params, "mode")
    ? params.mode
    : has(params, "xhttpMode") &&
        !(vmessJSON && raw.size && ["", "auto"].includes(params.xhttpMode))
      ? params.xhttpMode
      : raw.has("mode")
        ? JSON.parse(raw.get("mode"))
        : "auto";
  if (typeof selectedMode !== "string") invalid("mode", "expected a string");
  const mode = selectedMode || "auto";
  return {
    host: basic("host", ""),
    path: basic("path", ""),
    xhttpMode: mode,
    xhttpExtraJson: validateXhttpExtra(objectJSON(extra), mode),
  };
}

export function clearLegacyXhttpFields(model) {
  for (const key of [
    "extra",
    "mode",
    "xhttpRawJson",
    "xhttpExtraJson",
    "xhttpExtra",
    "xhttpHeaders",
    "x_padding_bytes",
    "xmuxHKeepAlive",
    ...integerFields,
    ...directRanges,
    ...booleanFields,
    ...stringFields,
  ])
    delete model[key];
  for (const [prefix] of rangeFields) {
    delete model[`${prefix}From`];
    delete model[`${prefix}To`];
  }
}
