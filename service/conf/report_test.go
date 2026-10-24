package conf

import "testing"

func TestReportNeedsDatabase(t *testing.T) {
	// Use only local report metadata: no environment parsing, database access,
	// core inspection or kernel commands are needed to classify a report.
	registered := types
	types = map[string]ReportType{}
	t.Cleanup(func() { types = registered })
	RegisterReportType(ReportType{Name: "stored", Func: func([]string) string {
		t.Fatal("classification invoked a report callback")
		return ""
	}})
	RegisterReportType(ReportType{Name: "metadata", NoDatabase: true})
	for _, test := range []struct {
		name string
		want bool
	}{
		{name: ""},
		{name: " \t\n "},
		{name: "unknown"},
		{name: "help"},
		{name: "metadata"},
		{name: "metadata with arguments"},
		{name: "stored", want: true},
		{name: "  stored\twith\narguments  ", want: true},
		{name: "STORED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := Params{PrintReport: test.name}
			if got := config.ReportNeedsDatabase(); got != test.want {
				t.Fatalf("ReportNeedsDatabase(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}
