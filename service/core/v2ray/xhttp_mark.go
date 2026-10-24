package v2ray

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Download connections can have their own socket options, so the uplink mark
// is not enough to keep split XHTTP traffic out of transparent proxy loops.
// Set only the mark on a copy; retain custom options and unknown JSON numbers.
func markXHTTPDownloads(extra json.RawMessage, mark, depth int) (json.RawMessage, error) {
	if depth > 16 {
		return nil, fmt.Errorf("download settings are nested too deeply")
	}
	if !gjson.ValidBytes(extra) || !gjson.ParseBytes(extra).IsObject() {
		return nil, fmt.Errorf("extra must be a JSON object")
	}
	download := gjson.GetBytes(extra, "downloadSettings")
	if !download.Exists() {
		return extra, nil
	}
	if !download.IsObject() {
		return nil, fmt.Errorf("downloadSettings must be a JSON object")
	}
	if options := download.Get("sockopt"); options.Exists() && options.Type != gjson.Null && !options.IsObject() {
		return nil, fmt.Errorf("downloadSettings.sockopt must be a JSON object")
	}
	stream, err := sjson.SetBytes([]byte(download.Raw), "sockopt.mark", mark)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"xhttpSettings", "splithttpSettings"} {
		settings := download.Get(key)
		if !settings.Exists() {
			continue
		}
		if !settings.IsObject() {
			return nil, fmt.Errorf("downloadSettings.%s must be a JSON object", key)
		}
		path, raw := key, settings.Raw
		if nested := settings.Get("extra"); nested.Exists() {
			path, raw = key+".extra", nested.Raw
		}
		updated, err := markXHTTPDownloads(json.RawMessage(raw), mark, depth+1)
		if err != nil {
			return nil, err
		}
		stream, err = sjson.SetRawBytes(stream, path, updated)
		if err != nil {
			return nil, err
		}
	}
	return sjson.SetRawBytes(extra, "downloadSettings", stream)
}
