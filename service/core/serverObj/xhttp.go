package serverObj

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/v2rayA/v2rayA/core/v2ray/where"
	"golang.org/x/mod/semver"
)

// Keep the core's extra object intact, rather than projecting it onto a fixed
// set of fields. RawMessage also preserves integers outside float64 precision.
type xhttpObject map[string]json.RawMessage

var xhttpRangeAliases = []struct{ from, to, field string }{
	{"scMaxEachPostBytesFrom", "scMaxEachPostBytesTo", "scMaxEachPostBytes"},
	{"scMinPostsIntervalFrom", "scMinPostsIntervalTo", "scMinPostsIntervalMs"},
	{"scStreamUpServerFrom", "scStreamUpServerTo", "scStreamUpServerSecs"},
	{"xPaddingBytesFrom", "xPaddingBytesTo", "xPaddingBytes"},
	{"xmuxMaxConcurFrom", "xmuxMaxConcurTo", "maxConcurrency"},
	{"xmuxMaxConnFrom", "xmuxMaxConnTo", "maxConnections"},
	{"xmuxCMaxReuseFrom", "xmuxCMaxReuseTo", "cMaxReuseTimes"},
	{"xmuxHMaxReqFrom", "xmuxHMaxReqTo", "hMaxRequestTimes"},
	{"xmuxHMaxReusableFrom", "xmuxHMaxReusableTo", "hMaxReusableSecs"},
}

var xhttpBoolFields = []string{"noGRPCHeader", "noSSEHeader", "xPaddingObfsMode"}
var xhttpStringFields = []string{
	"uplinkHTTPMethod", "xPaddingKey", "xPaddingHeader", "xPaddingPlacement", "xPaddingMethod",
	"sessionPlacement", "sessionKey", "sessionIDPlacement", "sessionIDKey", "sessionIDTable",
	"seqPlacement", "seqKey", "uplinkDataPlacement", "uplinkDataKey",
}
var xhttpRangeFields = []string{
	"scMaxEachPostBytes", "scMinPostsIntervalMs", "scStreamUpServerSecs", "xPaddingBytes",
	"sessionIDLength", "uplinkChunkSize",
}
var xhttpXmuxRanges = []string{"maxConcurrency", "maxConnections", "cMaxReuseTimes", "hMaxRequestTimes", "hMaxReusableSecs"}

func readXHTTPObject(raw []byte, field string) (xhttpObject, error) {
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '{' {
		return nil, fmt.Errorf("XHTTP %s must be a JSON object", field)
	}
	var object xhttpObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("invalid XHTTP %s: %w", field, err)
	}
	return object, nil
}

func xhttpJSON(value interface{}) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func xhttpString(raw json.RawMessage, field string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("XHTTP %s must be a string", field)
	}
	return value, nil
}

func (v *V2Ray) parseXHTTPQuery(q url.Values) error {
	v.Net = "xhttp"
	extra := xhttpObject{}
	legacy := xhttpObject{}
	if q.Has("xhttpRawJson") && q.Get("xhttpRawJson") != "" {
		var err error
		legacy, err = readXHTTPObject([]byte(q.Get("xhttpRawJson")), "xhttpRawJson")
		if err != nil {
			return err
		}
		for _, key := range []string{"host", "path", "mode"} {
			if raw, ok := legacy[key]; ok {
				value, err := xhttpString(raw, "xhttpRawJson."+key)
				if err != nil {
					return err
				}
				switch key {
				case "host":
					if !q.Has("host") {
						v.Host = value
					}
				case "path":
					if !q.Has("path") {
						v.Path = value
					}
				case "mode":
					v.XHTTPMode = value
				}
				delete(legacy, key)
			}
		}
		if nested, ok := legacy["extra"]; ok {
			object, err := readXHTTPObject(nested, "xhttpRawJson.extra")
			if err != nil {
				return err
			}
			delete(legacy, "extra")
			for key, raw := range object {
				legacy[key] = raw
			}
		}
		extra = legacy
	}
	// Presence matters: an explicit auto/empty mode or extra={} overrides the
	// legacy form. Flattened parameters then override individual extra fields.
	if q.Has("mode") {
		v.XHTTPMode = q.Get("mode")
	} else if q.Has("xhttpMode") {
		v.XHTTPMode = q.Get("xhttpMode")
	}
	if v.XHTTPMode == "" {
		v.XHTTPMode = "auto"
	}
	if q.Has("extra") {
		var err error
		extra, err = readXHTTPObject([]byte(q.Get("extra")), "extra")
		if err != nil {
			return err
		}
	}
	if raw := q.Get("x_padding_bytes"); q.Has("x_padding_bytes") {
		if _, exists := extra["xPaddingBytes"]; !exists {
			extra["xPaddingBytes"] = xhttpJSON(raw)
		}
	}
	for _, key := range xhttpBoolFields {
		if q.Has(key) {
			value, err := strconv.ParseBool(q.Get(key))
			if err != nil {
				return fmt.Errorf("invalid XHTTP %s: expected a boolean", key)
			}
			extra[key] = xhttpJSON(value)
		}
	}
	for _, key := range xhttpStringFields {
		if q.Has(key) {
			extra[key] = xhttpJSON(q.Get(key))
		}
	}
	for _, key := range xhttpRangeFields {
		if q.Has(key) {
			extra[key] = xhttpJSON(q.Get(key))
		}
	}
	for _, key := range []string{"scMaxBufferedPosts", "serverMaxHeaderBytes"} {
		if q.Has(key) {
			value, err := strconv.ParseInt(q.Get(key), 10, 64)
			if err != nil {
				return fmt.Errorf("invalid XHTTP %s: expected an integer", key)
			}
			extra[key] = xhttpJSON(value)
		}
	}
	if q.Has("xhttpHeaders") {
		object, err := readXHTTPObject([]byte(q.Get("xhttpHeaders")), "xhttpHeaders")
		if err != nil {
			return err
		}
		extra["headers"] = xhttpJSON(object)
	}
	xmux := xhttpObject{}
	if raw, exists := extra["xmux"]; exists {
		var err error
		xmux, err = readXHTTPObject(raw, "xmux")
		if err != nil {
			return err
		}
	}
	xmuxChanged := false
	for _, alias := range xhttpRangeAliases {
		if !q.Has(alias.from) && !q.Has(alias.to) {
			continue
		}
		left, right := q.Get(alias.from), q.Get(alias.to)
		if !q.Has(alias.from) {
			left = right
		}
		if !q.Has(alias.to) {
			right = left
		}
		from, err := strconv.ParseInt(left, 10, 32)
		if err != nil {
			return fmt.Errorf("invalid XHTTP %s: expected an int32", alias.from)
		}
		to, err := strconv.ParseInt(right, 10, 32)
		if err != nil {
			return fmt.Errorf("invalid XHTTP %s: expected an int32", alias.to)
		}
		raw := xhttpJSON(from)
		if from != to {
			raw = xhttpJSON(fmt.Sprintf("%d-%d", from, to))
		}
		if strings.HasPrefix(alias.from, "xmux") {
			xmux[alias.field], xmuxChanged = raw, true
		} else {
			extra[alias.field] = raw
		}
	}
	if q.Has("xmuxHKeepAlive") {
		value, err := strconv.ParseInt(q.Get("xmuxHKeepAlive"), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid XHTTP xmuxHKeepAlive: expected an integer")
		}
		xmux["hKeepAlivePeriod"], xmuxChanged = xhttpJSON(value), true
	}
	if xmuxChanged {
		extra["xmux"] = xhttpJSON(xmux)
	}
	v.XHTTPExtra = xhttpJSON(extra)
	return validateXHTTPObject(extra, v.XHTTPMode, 0)
}

// JSON storage and VMess links share this decoder. Existing records that have
// no canonical extra can recover the original VLESS URI, including old GUI's
// complete xhttpRawJson object. New records always carry an object, even {}.
func (v *V2Ray) restoreXHTTPJSON(data []byte) error {
	var fields xhttpObject
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields["xhttpExtra"]; exists {
		extra, err := readXHTTPObject(raw, "xhttpExtra")
		if err != nil {
			return err
		}
		if v.XHTTPMode == "" {
			v.XHTTPMode = "auto"
		}
		v.Net = "xhttp"
		return validateXHTTPObject(extra, v.XHTTPMode, 0)
	}
	q := url.Values{}
	if source, err := url.Parse(v.V); err == nil && source.Scheme == "vless" {
		q = source.Query()
	}
	keys := []string{"mode", "xhttpRawJson", "extra", "xhttpHeaders", "x_padding_bytes", "xmuxHKeepAlive", "scMaxBufferedPosts", "serverMaxHeaderBytes"}
	keys = append(keys, xhttpBoolFields...)
	keys = append(keys, xhttpStringFields...)
	keys = append(keys, xhttpRangeFields...)
	for _, alias := range xhttpRangeAliases {
		keys = append(keys, alias.from, alias.to)
	}
	for _, key := range keys {
		if raw, ok := fields[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				value = string(raw)
			}
			q.Set(key, value)
		}
	}
	// Preserve saved basic edits; old parsers supplied auto even when a
	// standard mode was present only in V, so recover that specific case.
	for _, key := range []string{"host", "path", "xhttpMode"} {
		if raw, ok := fields[key]; ok {
			value, err := xhttpString(raw, key)
			if err != nil {
				return err
			}
			if key == "xhttpMode" && value == "auto" && !q.Has("xhttpMode") && (q.Has("mode") || q.Has("xhttpRawJson")) {
				continue
			}
			if key == "xhttpMode" && value != "" {
				if _, explicitMode := fields["mode"]; !explicitMode {
					q.Del("mode")
				}
			}
			if value != "" || !q.Has("xhttpRawJson") {
				q.Set(key, value)
			}
		}
	}
	if q.Has("host") {
		v.Host = q.Get("host")
	}
	if q.Has("path") {
		v.Path = q.Get("path")
	}
	return v.parseXHTTPQuery(q)
}

func parseXHTTPRange(raw json.RawMessage, field string) (int64, int64, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		value = string(raw)
	}
	if single, err := strconv.ParseInt(value, 10, 32); err == nil {
		return single, single, nil
	}
	start := 0
	if strings.HasPrefix(value, "-") {
		start = 1
	}
	dash := strings.Index(value[start:], "-")
	if dash >= 0 {
		dash += start
		left, e1 := strconv.ParseInt(value[:dash], 10, 32)
		right, e2 := strconv.ParseInt(value[dash+1:], 10, 32)
		if e1 == nil && e2 == nil {
			return left, right, nil
		}
	}
	return 0, 0, fmt.Errorf("XHTTP %s must be an int32 or an int32 range string", field)
}

func validateXHTTPObject(extra xhttpObject, mode string, depth int) error {
	if depth > 16 {
		return fmt.Errorf("XHTTP downloadSettings nesting is too deep")
	}
	switch mode {
	case "", "auto", "packet-up", "stream-up", "stream-one":
	default:
		return fmt.Errorf("unsupported XHTTP mode %q", mode)
	}
	for _, key := range xhttpRangeFields {
		if raw, ok := extra[key]; ok {
			from, to, err := parseXHTTPRange(raw, key)
			if err != nil {
				return err
			}
			if key == "scMaxEachPostBytes" && (from != 0 || to != 0) && (from <= 0 || to <= 0) {
				return fmt.Errorf("XHTTP %s must have positive endpoints, or be 0 for the core default", key)
			}
			if key == "xPaddingBytes" && (from < 0 || to < 0) {
				return fmt.Errorf("XHTTP xPaddingBytes cannot be negative")
			}
		}
	}
	for _, key := range []string{"scMaxBufferedPosts", "serverMaxHeaderBytes"} {
		if raw, exists := extra[key]; exists {
			var value int64
			if err := json.Unmarshal(raw, &value); err != nil || value < 0 || bytes.Equal(raw, []byte("null")) {
				return fmt.Errorf("XHTTP %s must be a non-negative integer", key)
			}
			if key == "serverMaxHeaderBytes" && value > 1<<31-1 {
				return fmt.Errorf("XHTTP serverMaxHeaderBytes exceeds int32")
			}
		}
	}
	for _, key := range xhttpBoolFields {
		if raw, exists := extra[key]; exists && !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
			return fmt.Errorf("XHTTP %s must be a boolean", key)
		}
	}
	for _, key := range xhttpStringFields {
		if raw, exists := extra[key]; exists {
			if _, err := xhttpString(raw, key); err != nil {
				return err
			}
		}
	}
	if raw, exists := extra["sessionIDTable"]; exists {
		table, _ := xhttpString(raw, "sessionIDTable")
		if table != "" {
			from, to, err := parseXHTTPRange(extra["sessionIDLength"], "sessionIDLength")
			// Xray's roomSize loops with an int32 counter through the upper
			// endpoint. MaxInt32 wraps that counter, so even -test never ends.
			if err != nil || from <= 0 || to <= 0 || from == 1<<31-1 || to == 1<<31-1 {
				return fmt.Errorf("XHTTP sessionIDTable requires positive sessionIDLength endpoints below 2147483647")
			}
		}
	}
	if raw, exists := extra["headers"]; exists {
		headers, err := readXHTTPObject(raw, "headers")
		if err != nil {
			return err
		}
		for key, raw := range headers {
			if strings.EqualFold(key, "host") {
				return fmt.Errorf("XHTTP headers cannot contain Host; use the host field")
			}
			if _, err := xhttpString(raw, "headers."+key); err != nil {
				return err
			}
		}
	}
	if raw, exists := extra["xmux"]; exists {
		xmux, err := readXHTTPObject(raw, "xmux")
		if err != nil {
			return err
		}
		positive := map[string]bool{}
		for _, key := range xhttpXmuxRanges {
			if raw, exists := xmux[key]; exists {
				from, to, err := parseXHTTPRange(raw, "xmux."+key)
				if err != nil {
					return err
				}
				positive[key] = from > 0 || to > 0
			}
		}
		if positive["maxConcurrency"] && positive["maxConnections"] {
			return fmt.Errorf("XHTTP xmux maxConcurrency and maxConnections cannot both be positive")
		}
		if raw, exists := xmux["hKeepAlivePeriod"]; exists {
			var value int64
			if err := json.Unmarshal(raw, &value); err != nil || bytes.Equal(raw, []byte("null")) {
				return fmt.Errorf("XHTTP xmux.hKeepAlivePeriod must be an integer")
			}
		}
	}
	if raw, exists := extra["downloadSettings"]; exists {
		if mode == "stream-one" {
			return fmt.Errorf("XHTTP stream-one cannot use downloadSettings")
		}
		download, err := readXHTTPObject(raw, "downloadSettings")
		if err != nil {
			return err
		}
		// The core's config test accepts a missing destination, but its XHTTP
		// dialer dereferences memory2.Destination without checking for nil.
		address, err := xhttpString(download["address"], "downloadSettings.address")
		if err != nil || strings.TrimSpace(address) == "" {
			return fmt.Errorf("XHTTP downloadSettings.address must be a non-empty string")
		}
		var port uint16
		if err := json.Unmarshal(download["port"], &port); err != nil || port == 0 {
			return fmt.Errorf("XHTTP downloadSettings.port must be an integer from 1 to 65535")
		}
		network := download["network"]
		if method, exists := download["method"]; exists {
			network = method
		}
		method, err := xhttpString(network, "downloadSettings.network")
		if err != nil || (method != "xhttp" && method != "splithttp") {
			return fmt.Errorf("XHTTP downloadSettings.network must be xhttp")
		}
		settings := download["xhttpSettings"]
		if settings == nil {
			settings = download["splithttpSettings"]
		}
		if settings != nil {
			object, err := readXHTTPObject(settings, "downloadSettings.xhttpSettings")
			if err != nil {
				return err
			}
			nestedMode := "auto"
			if raw, exists := object["mode"]; exists {
				nestedMode, err = xhttpString(raw, "downloadSettings.xhttpSettings.mode")
				if err != nil {
					return err
				}
			}
			if raw, exists := object["extra"]; exists {
				object, err = readXHTTPObject(raw, "downloadSettings.xhttpSettings.extra")
				if err != nil {
					return err
				}
			}
			if err := validateXHTTPObject(object, nestedMode, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *V2Ray) validateXHTTPCore(info PriorInfo) error {
	return ValidateXHTTPCore(v.XHTTPMode, v.XHTTPExtra, info)
}

// ValidateXHTTPCore checks an XHTTP object against its actual target core,
// including nested download settings, without modifying the supplied JSON.
func ValidateXHTTPCore(mode string, rawExtra json.RawMessage, info PriorInfo) error {
	if info.Variant != where.Xray {
		return fmt.Errorf("XHTTP requires Xray; please select an Xray core")
	}
	extra := xhttpObject{}
	if len(rawExtra) != 0 {
		var err error
		extra, err = readXHTTPObject(rawExtra, "extra")
		if err != nil {
			return err
		}
	}
	if err := validateXHTTPObject(extra, mode, 0); err != nil {
		return err
	}
	version := "v" + strings.TrimPrefix(info.CoreVersion, "v")
	if !semver.IsValid(version) {
		return nil
	}
	minimum := "v24.10.31" // Xray #3955 introduced XHTTP and download splitting.
	if len(extra) != 0 || mode == "stream-up" {
		minimum = "v24.11.11"
	} // Xray #4000 introduced extra.
	if mode == "stream-one" {
		minimum = "v24.11.30"
	} // Xray #4071.
	raise := func(required string) {
		if semver.Compare(required, minimum) > 0 {
			minimum = required
		}
	}
	if _, ok := extra["noGRPCHeader"]; ok {
		raise("v24.11.21")
	}
	if _, ok := extra["scMaxBufferedPosts"]; ok {
		raise("v24.12.15")
	}
	if _, ok := extra["scStreamUpServerSecs"]; ok {
		raise("v25.1.30")
	} // Xray #4306.
	if raw, ok := extra["xmux"]; ok {
		xmux, _ := readXHTTPObject(raw, "xmux")
		for _, key := range []string{"hMaxRequestTimes", "hKeepAlivePeriod"} {
			if _, ok := xmux[key]; ok {
				raise("v24.12.15")
			}
		}
		if _, ok := xmux["hMaxReusableSecs"]; ok {
			raise("v24.12.31")
		}
	}
	for _, key := range append(append([]string{}, xhttpStringFields...), "xPaddingObfsMode", "uplinkChunkSize") {
		if _, ok := extra[key]; ok {
			raise("v26.1.31")
		} // Xray #5414.
	}
	for _, key := range []string{"sessionIDPlacement", "sessionIDKey", "sessionIDTable", "sessionIDLength"} {
		if _, ok := extra[key]; ok {
			raise("v26.6.22")
		} // Xray #6258.
	}
	if semver.Compare(version, minimum) < 0 {
		return fmt.Errorf("these XHTTP settings require Xray %s or newer; detected %s", strings.TrimPrefix(minimum, "v"), info.CoreVersion)
	}
	// Since #5414 the core rejects a partially zero padding range, while 0
	// still means its default. Preserve the input and report the incompatibility.
	if raw, ok := extra["xPaddingBytes"]; ok && semver.Compare(version, "v26.1.31") >= 0 {
		from, to, _ := parseXHTTPRange(raw, "xPaddingBytes")
		if (from != 0 || to != 0) && (from == 0 || to == 0) {
			return fmt.Errorf("Xray %s does not support a zero endpoint in xPaddingBytes", info.CoreVersion)
		}
	}
	if raw, exists := extra["downloadSettings"]; exists {
		download, _ := readXHTTPObject(raw, "downloadSettings")
		// The method alias was added in Xray #6426. Older versions silently
		// ignore it and default the download transport to TCP.
		if _, exists := download["method"]; exists && semver.Compare(version, "v26.7.11") < 0 {
			return fmt.Errorf("XHTTP downloadSettings.method requires Xray 26.7.11 or newer; use network with older cores")
		}
		settings := download["xhttpSettings"]
		if settings == nil {
			settings = download["splithttpSettings"]
		}
		if settings != nil {
			object, _ := readXHTTPObject(settings, "downloadSettings.xhttpSettings")
			nested := &V2Ray{XHTTPMode: "auto"}
			if raw, exists := object["mode"]; exists {
				nested.XHTTPMode, _ = xhttpString(raw, "mode")
			}
			if raw, exists := object["extra"]; exists {
				nested.XHTTPExtra = raw
			} else {
				nested.XHTTPExtra = xhttpJSON(object)
			}
			if err := nested.validateXHTTPCore(info); err != nil {
				return fmt.Errorf("XHTTP downloadSettings: %w", err)
			}
		}
	}
	return nil
}
