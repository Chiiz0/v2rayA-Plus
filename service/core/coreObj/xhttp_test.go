package coreObj

import (
	"encoding/json"
	"strings"
	"testing"

	jsoniter "github.com/json-iterator/go"
)

func TestXHTTPExtraRemainsAnObjectInCoreConfig(t *testing.T) {
	settings := XHTTPSettings{
		Host: "example.com", Path: "/test", Mode: "packet-up",
		Extra: json.RawMessage(`{"noGRPCHeader":false,"scMinPostsIntervalMs":0,"xmux":{"hKeepAlivePeriod":-1},"future":9007199254740993}`),
	}
	for name, marshal := range map[string]func(interface{}) ([]byte, error){"standard": json.Marshal, "configuration": jsoniter.Marshal} {
		t.Run(name, func(t *testing.T) {
			data, err := marshal(StreamSettings{Network: "xhttp", XHTTPSettings: &settings})
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{`"extra":{`, `"noGRPCHeader":false`, `"scMinPostsIntervalMs":0`, `"hKeepAlivePeriod":-1`, `"future":9007199254740993`} {
				if !strings.Contains(string(data), expected) {
					t.Fatalf("generated config lost %s: %s", expected, data)
				}
			}
		})
	}
}
