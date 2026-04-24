package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func validForm() *FormSection {
	return &FormSection{
		Title:     "Chat",
		ConfigKey: "chat",
		Fields: map[string]FieldSchema{
			"system_prompt": {Type: FieldTextarea, Label: "System Prompt"},
		},
	}
}

func TestValidateDescriptor_Minimal(t *testing.T) {
	d := &PanelDescriptor{
		Version: 1,
		Title:   "Chat",
		Sections: []Section{
			{Kind: KindForm, Form: validForm()},
		},
	}
	if err := ValidateDescriptor(d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDescriptor_RejectsNil(t *testing.T) {
	if err := ValidateDescriptor(nil); err == nil {
		t.Fatal("expected error for nil descriptor")
	}
}

func TestValidateDescriptor_RejectsWrongVersion(t *testing.T) {
	d := &PanelDescriptor{
		Version:  2,
		Title:    "X",
		Sections: []Section{{Kind: KindForm, Form: validForm()}},
	}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestValidateDescriptor_RejectsEmptyTitle(t *testing.T) {
	d := &PanelDescriptor{
		Version:  1,
		Title:    "",
		Sections: []Section{{Kind: KindForm, Form: validForm()}},
	}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestValidateDescriptor_RejectsEmptySections(t *testing.T) {
	d := &PanelDescriptor{Version: 1, Title: "X", Sections: []Section{}}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for empty sections")
	}
}

func TestValidateDescriptor_UnknownSectionKind(t *testing.T) {
	d := &PanelDescriptor{
		Version:  1,
		Title:    "X",
		Sections: []Section{{Kind: SectionKind("bogus")}},
	}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestValidateForm_RejectsNoFields(t *testing.T) {
	d := &PanelDescriptor{
		Version: 1, Title: "X",
		Sections: []Section{{
			Kind: KindForm,
			Form: &FormSection{Title: "T", ConfigKey: "k", Fields: map[string]FieldSchema{}},
		}},
	}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for empty fields")
	}
}

func TestValidateForm_RejectsEnumWithoutValues(t *testing.T) {
	d := &PanelDescriptor{
		Version: 1, Title: "X",
		Sections: []Section{{
			Kind: KindForm,
			Form: &FormSection{Title: "T", ConfigKey: "k",
				Fields: map[string]FieldSchema{"x": {Type: FieldEnum}},
			},
		}},
	}
	if err := ValidateDescriptor(d); err == nil || !strings.Contains(err.Error(), "values") {
		t.Fatalf("expected error about enum values, got %v", err)
	}
}

func TestValidateForm_RejectsUnknownFieldType(t *testing.T) {
	d := &PanelDescriptor{
		Version: 1, Title: "X",
		Sections: []Section{{
			Kind: KindForm,
			Form: &FormSection{Title: "T", ConfigKey: "k",
				Fields: map[string]FieldSchema{"x": {Type: FieldType("weird")}},
			},
		}},
	}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for unknown field type")
	}
}

func TestValidateIframe_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body *IframeSection
	}{
		{"no title", &IframeSection{URL: "http://x"}},
		{"no url", &IframeSection{Title: "T"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &PanelDescriptor{Version: 1, Title: "X", Sections: []Section{{Kind: KindIframe, Iframe: tc.body}}}
			if err := ValidateDescriptor(d); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestValidateStatus_RequiresLayout(t *testing.T) {
	d := &PanelDescriptor{Version: 1, Title: "X",
		Sections: []Section{{Kind: KindStatus, Status: &StatusSection{Title: "T", URL: "/x"}}}}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for missing layout")
	}
}

func TestValidateStatus_UnknownLayout(t *testing.T) {
	d := &PanelDescriptor{Version: 1, Title: "X",
		Sections: []Section{{Kind: KindStatus, Status: &StatusSection{Title: "T", URL: "/x", Layout: "grid"}}}}
	if err := ValidateDescriptor(d); err == nil {
		t.Fatal("expected error for unknown layout")
	}
}

func TestClampOrder_BuiltinUnclamped(t *testing.T) {
	d := &PanelDescriptor{Order: ptr(10)}
	ord, ok := ClampOrder(d, false)
	if !ok || ord != 10 {
		t.Fatalf("got (%d, %v), want (10, true)", ord, ok)
	}
}

func TestClampOrder_ServiceClamped(t *testing.T) {
	d := &PanelDescriptor{Order: ptr(5)}
	ord, ok := ClampOrder(d, true)
	if !ok || ord != ReservedOrderCeiling {
		t.Fatalf("got (%d, %v); want (%d, true)", ord, ok, ReservedOrderCeiling)
	}
}

func TestClampOrder_NilReturnsFalse(t *testing.T) {
	if _, ok := ClampOrder(nil, true); ok {
		t.Fatal("nil descriptor should return ok=false")
	}
	d := &PanelDescriptor{Order: nil}
	if _, ok := ClampOrder(d, true); ok {
		t.Fatal("nil order should return ok=false")
	}
}

// JSON polymorphism round-trips.

func TestSectionJSON_FormRoundTrip(t *testing.T) {
	orig := Section{Kind: KindForm, Form: validForm()}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// envelope must carry kind:"form" plus form fields flattened alongside
	if !strings.Contains(string(data), `"kind":"form"`) {
		t.Fatalf("missing kind discriminator: %s", data)
	}
	if !strings.Contains(string(data), `"config_key":"chat"`) {
		t.Fatalf("missing config_key: %s", data)
	}
	var back Section
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Kind != KindForm || back.Form == nil || back.Form.ConfigKey != "chat" {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}
}

func TestSectionJSON_IframeRoundTrip(t *testing.T) {
	orig := Section{Kind: KindIframe, Iframe: &IframeSection{Title: "CP", URL: "http://x:9999", Height: "800px"}}
	data, _ := json.Marshal(orig)
	var back Section
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Iframe.URL != "http://x:9999" || back.Iframe.Height != "800px" {
		t.Fatalf("roundtrip: %+v", back.Iframe)
	}
}

func TestSectionJSON_StatusRoundTrip(t *testing.T) {
	orig := Section{Kind: KindStatus, Status: &StatusSection{Title: "Sys", URL: "/v1/status", Layout: StatusKeyValue}}
	data, _ := json.Marshal(orig)
	var back Section
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Status.Layout != StatusKeyValue {
		t.Fatalf("layout: %q", back.Status.Layout)
	}
}

func TestSectionJSON_UnknownKindRejected(t *testing.T) {
	data := []byte(`{"kind":"bogus","title":"x"}`)
	var s Section
	if err := json.Unmarshal(data, &s); err == nil {
		t.Fatal("expected error on unmarshal of unknown kind")
	}
}

func TestSectionJSON_MissingKindRejected(t *testing.T) {
	data := []byte(`{"title":"x","url":"/y","layout":"key_value"}`)
	var s Section
	if err := json.Unmarshal(data, &s); err == nil {
		t.Fatal("expected error for missing kind discriminator")
	}
}

func TestDescriptorRoundTripWithAllKinds(t *testing.T) {
	d := &PanelDescriptor{
		Version: 1,
		Title:   "Mixed",
		Order:   ptr(150),
		Sections: []Section{
			{Kind: KindForm, Form: validForm()},
			{Kind: KindIframe, Iframe: &IframeSection{Title: "CP", URL: "http://x"}},
			{Kind: KindStatus, Status: &StatusSection{Title: "Stats", URL: "/v1/status", Layout: StatusTable}},
		},
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back PanelDescriptor
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := ValidateDescriptor(&back); err != nil {
		t.Fatalf("roundtripped descriptor failed validation: %v", err)
	}
	if len(back.Sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(back.Sections))
	}
}
