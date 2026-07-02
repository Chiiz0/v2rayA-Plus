package common_test

import (
	"testing"

	"github.com/v2rayA/v2rayA/common"
)

func TestUrlEncoded(t *testing.T) {
	str := `试试1+就试试!`
	t.Log(common.UrlEncoded(str))
}

func TestFillEmpty(t *testing.T) {
	type sample struct {
		Name    string
		Count   int
		Enabled bool
	}

	target := &sample{Name: "kept"}
	defaultValue := sample{Name: "default", Count: 7, Enabled: true}
	if err := common.FillEmpty(target, defaultValue); err != nil {
		t.Fatal(err)
	}
	if target.Name != "kept" {
		t.Fatalf("Name = %q, want kept", target.Name)
	}
	if target.Count != defaultValue.Count {
		t.Fatalf("Count = %d, want %d", target.Count, defaultValue.Count)
	}
	if target.Enabled {
		t.Fatal("Enabled was overwritten; bool fields should be ignored")
	}
}
