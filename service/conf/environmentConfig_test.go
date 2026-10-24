package conf

import "testing"

func TestResolveDatabaseBackend(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"", "auto"}, {"auto", "auto"}, {" BOLT ", "bolt"}, {"SQLite", "sqlite"},
	} {
		got, err := resolveDatabaseBackend(test.input)
		if err != nil || got != test.want {
			t.Fatalf("resolveDatabaseBackend(%q) = %q, %v", test.input, got, err)
		}
	}
	if _, err := resolveDatabaseBackend("postgres"); err == nil {
		t.Fatal("unsupported database backend accepted")
	}
}

func TestResolveCoreSelection(t *testing.T) {
	tests := []struct {
		name       string
		core       string
		binary     string
		wantCore   string
		wantBinary string
		wantErr    bool
	}{
		{name: "empty is auto", wantCore: "auto"},
		{name: "auto keeps discovery", core: "auto", wantCore: "auto"},
		{name: "v2fly selects v2ray", core: "V2Fly", wantCore: "v2fly", wantBinary: "v2ray"},
		{name: "xray selects xray", core: " xray ", wantCore: "xray", wantBinary: "xray"},
		{name: "explicit binary wins", core: "xray", binary: "/opt/custom-core", wantCore: "xray", wantBinary: "/opt/custom-core"},
		{name: "invalid core", core: "sing-box", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCore, gotBinary, err := resolveCoreSelection(tt.core, tt.binary)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveCoreSelection() error = %v, wantErr %v", err, tt.wantErr)
			}
			if gotCore != tt.wantCore || gotBinary != tt.wantBinary {
				t.Fatalf("resolveCoreSelection() = (%q, %q), want (%q, %q)", gotCore, gotBinary, tt.wantCore, tt.wantBinary)
			}
		})
	}
}
