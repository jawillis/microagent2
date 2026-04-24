package memoryservice

import (
	"testing"

	"microagent2/internal/dashboard"
)

func TestBuildPanelDescriptor_Valid(t *testing.T) {
	d := BuildPanelDescriptor("http://localhost:9999", "microagent2", "http://memory-service:8083/status")
	if err := dashboard.ValidateDescriptor(d); err != nil {
		t.Fatalf("descriptor fails validation: %v", err)
	}
	if d.Title != "Memory" {
		t.Fatalf("title = %q", d.Title)
	}
	if d.Order == nil || *d.Order != 200 {
		t.Fatalf("order = %v, want 200", d.Order)
	}
	if len(d.Sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(d.Sections))
	}
}

func TestBuildPanelDescriptor_FormHasExpectedFields(t *testing.T) {
	d := BuildPanelDescriptor("http://localhost:9999", "microagent2", "http://memory-service:8083/status")
	form := d.Sections[0].Form
	if form == nil {
		t.Fatal("first section must be form")
	}
	if form.ConfigKey != "memory" {
		t.Fatalf("config_key = %q", form.ConfigKey)
	}
	want := []string{"recall_limit", "prewarm_limit", "recall_default_types", "default_provenance", "tag_taxonomy", "memory_bank_id"}
	for _, name := range want {
		if _, ok := form.Fields[name]; !ok {
			t.Errorf("missing field %q", name)
		}
	}
}

func TestBuildPanelDescriptor_BankIDInReadonlyField(t *testing.T) {
	d := BuildPanelDescriptor("http://localhost:9999", "my-bank", "http://memory-service:8083/status")
	form := d.Sections[0].Form
	f := form.Fields["memory_bank_id"]
	if !f.Readonly {
		t.Fatal("memory_bank_id must be readonly")
	}
	if f.Default != "my-bank" {
		t.Fatalf("bank default = %v", f.Default)
	}
}

func TestBuildPanelDescriptor_IframeURLCarriesCP(t *testing.T) {
	d := BuildPanelDescriptor("http://example.com:9999", "microagent2", "http://memory-service:8083/status")
	iframe := d.Sections[2].Iframe
	if iframe == nil {
		t.Fatal("second section must be iframe")
	}
	if iframe.URL != "http://example.com:9999" {
		t.Fatalf("url = %q", iframe.URL)
	}
	if iframe.Height != "800px" {
		t.Fatalf("height = %q", iframe.Height)
	}
}

func TestBuildPanelDescriptor_EnumsMatchConfig(t *testing.T) {
	d := BuildPanelDescriptor("x", "b", "http://memory-service:8083/status")
	form := d.Sections[0].Form
	rt := form.Fields["recall_default_types"]
	if len(rt.Values) != 3 {
		t.Fatalf("recall_default_types values: %v", rt.Values)
	}
	prov := form.Fields["default_provenance"]
	if len(prov.Values) != 4 {
		t.Fatalf("default_provenance values: %v", prov.Values)
	}
}
