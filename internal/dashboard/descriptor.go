// Package dashboard holds the declarative panel descriptor types that
// services publish at registration time to describe their presence in
// the microagent2 dashboard. The gateway aggregates descriptors from
// registered services and returns them via GET /v1/dashboard/panels;
// the dashboard shell renders panels by interpreting the descriptors.
package dashboard

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CurrentDescriptorVersion is the latest descriptor schema version this
// package understands. Services declare their descriptor's version so
// older gateways can skip descriptors they don't know how to render.
const CurrentDescriptorVersion = 1

// ReservedOrderCeiling is the upper bound of the order range reserved
// for gateway built-in panels. Service-contributed descriptors with an
// order below this are clamped to ReservedOrderCeiling.
const ReservedOrderCeiling = 100

// SectionKind enumerates the closed set of section types descriptors
// may declare. Additional kinds are added by future capability changes
// (e.g. logs, action) and this enum grows accordingly.
type SectionKind string

const (
	KindForm   SectionKind = "form"
	KindIframe SectionKind = "iframe"
	KindStatus SectionKind = "status"
	KindLogs   SectionKind = "logs"
)

// FieldType enumerates the closed set of form-field types supported by
// the schema dialect. Enums have a `values` list; numeric types support
// min/max/step.
type FieldType string

const (
	FieldString   FieldType = "string"
	FieldNumber   FieldType = "number"
	FieldInteger  FieldType = "integer"
	FieldBoolean  FieldType = "boolean"
	FieldEnum     FieldType = "enum"
	FieldTextarea FieldType = "textarea"
)

// StatusLayout enumerates the display shapes of a status section.
type StatusLayout string

const (
	StatusKeyValue StatusLayout = "key_value"
	StatusTable    StatusLayout = "table"
)

// PanelDescriptor is the top-level declaration a service publishes.
type PanelDescriptor struct {
	Version  int       `json:"version"`
	Title    string    `json:"title"`
	Order    *int      `json:"order,omitempty"`
	Sections []Section `json:"sections"`
}

// Section is one rendered block inside a panel. The Kind field
// discriminates the variant; marshal/unmarshal handle the polymorphism.
type Section struct {
	Kind SectionKind
	// Exactly one of the following is populated, matching Kind.
	Form   *FormSection
	Iframe *IframeSection
	Status *StatusSection
	Logs   *LogsSection
}

// FormSection declares a config form backed by the gateway's existing
// PUT /v1/config endpoint. ConfigKey is the section name passed through
// (e.g. "chat", "memory"). Fields maps field name → schema.
type FormSection struct {
	Title     string                 `json:"title"`
	ConfigKey string                 `json:"config_key"`
	Fields    map[string]FieldSchema `json:"fields"`
}

// IframeSection declares an embedded URL. Height defaults to "600px" on
// render if empty.
type IframeSection struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Height string `json:"height,omitempty"`
}

// StatusSection declares a read-only view of data fetched from URL.
type StatusSection struct {
	Title  string       `json:"title"`
	URL    string       `json:"url"`
	Layout StatusLayout `json:"layout"`
}

// LogsSection declares a live-tailing log viewer. The dashboard opens
// an EventSource to TailURL and fetches HistoryURL for initial context;
// ServicesURL returns the list of discoverable services for the
// filter UI.
type LogsSection struct {
	Title           string   `json:"title"`
	TailURL         string   `json:"tail_url"`
	HistoryURL      string   `json:"history_url"`
	ServicesURL     string   `json:"services_url"`
	DefaultServices []string `json:"default_services,omitempty"`
	DefaultLevel    string   `json:"default_level,omitempty"`
}

// FieldSchema declares a single form field's type and constraints.
// Only the fields relevant to Type are meaningful; validators reject
// combinations that don't make sense (e.g. an enum without Values).
type FieldSchema struct {
	Type        FieldType `json:"type"`
	Label       string    `json:"label,omitempty"`
	Description string    `json:"description,omitempty"`
	Default     any       `json:"default,omitempty"`
	Readonly    bool      `json:"readonly,omitempty"`

	// Numeric constraints (applied to number and integer types).
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Step *float64 `json:"step,omitempty"`

	// Enum values (required when Type == FieldEnum).
	Values []string `json:"values,omitempty"`
}

// ValidateDescriptor checks a descriptor against the schema rules and
// returns a human-readable error on the first violation. A nil error
// means the descriptor is safe to expose via the aggregation endpoint.
//
// Descriptors with a Version higher than CurrentDescriptorVersion are
// rejected here; callers that want forward-compatibility (tolerate
// newer versions by skipping) should check the version before calling.
func ValidateDescriptor(d *PanelDescriptor) error {
	if d == nil {
		return fmt.Errorf("descriptor is nil")
	}
	if d.Version != CurrentDescriptorVersion {
		return fmt.Errorf("unsupported descriptor version %d (want %d)", d.Version, CurrentDescriptorVersion)
	}
	if d.Title == "" {
		return fmt.Errorf("descriptor title is required")
	}
	if len(d.Sections) == 0 {
		return fmt.Errorf("descriptor must have at least one section")
	}
	for i, s := range d.Sections {
		if err := validateSection(s); err != nil {
			return fmt.Errorf("section %d: %w", i, err)
		}
	}
	return nil
}

func validateSection(s Section) error {
	switch s.Kind {
	case KindForm:
		if s.Form == nil {
			return fmt.Errorf("kind=form requires form body")
		}
		return validateForm(s.Form)
	case KindIframe:
		if s.Iframe == nil {
			return fmt.Errorf("kind=iframe requires iframe body")
		}
		return validateIframe(s.Iframe)
	case KindStatus:
		if s.Status == nil {
			return fmt.Errorf("kind=status requires status body")
		}
		return validateStatus(s.Status)
	case KindLogs:
		if s.Logs == nil {
			return fmt.Errorf("kind=logs requires logs body")
		}
		return validateLogs(s.Logs)
	case "":
		return fmt.Errorf("section kind is required")
	default:
		return fmt.Errorf("unknown section kind %q", s.Kind)
	}
}

func validateLogs(l *LogsSection) error {
	if l.Title == "" {
		return fmt.Errorf("logs.title is required")
	}
	if l.TailURL == "" {
		return fmt.Errorf("logs.tail_url is required")
	}
	if l.HistoryURL == "" {
		return fmt.Errorf("logs.history_url is required")
	}
	if l.ServicesURL == "" {
		return fmt.Errorf("logs.services_url is required")
	}
	if l.DefaultLevel != "" {
		switch strings.ToLower(l.DefaultLevel) {
		case "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("logs.default_level must be one of debug|info|warn|error; got %q", l.DefaultLevel)
		}
	}
	return nil
}

func validateForm(f *FormSection) error {
	if f.Title == "" {
		return fmt.Errorf("form.title is required")
	}
	if f.ConfigKey == "" {
		return fmt.Errorf("form.config_key is required")
	}
	if len(f.Fields) == 0 {
		return fmt.Errorf("form.fields must have at least one entry")
	}
	for name, fs := range f.Fields {
		if err := validateField(fs); err != nil {
			return fmt.Errorf("form.fields[%s]: %w", name, err)
		}
	}
	return nil
}

func validateIframe(i *IframeSection) error {
	if i.Title == "" {
		return fmt.Errorf("iframe.title is required")
	}
	if i.URL == "" {
		return fmt.Errorf("iframe.url is required")
	}
	return nil
}

func validateStatus(s *StatusSection) error {
	if s.Title == "" {
		return fmt.Errorf("status.title is required")
	}
	if s.URL == "" {
		return fmt.Errorf("status.url is required")
	}
	switch s.Layout {
	case StatusKeyValue, StatusTable:
		return nil
	case "":
		return fmt.Errorf("status.layout is required")
	default:
		return fmt.Errorf("unknown status layout %q", s.Layout)
	}
}

func validateField(f FieldSchema) error {
	switch f.Type {
	case FieldString, FieldNumber, FieldInteger, FieldBoolean, FieldTextarea:
		// ok; numeric constraints only meaningful for number/integer but
		// are not intrinsically invalid on other types.
		return nil
	case FieldEnum:
		if len(f.Values) == 0 {
			return fmt.Errorf("enum field requires non-empty values")
		}
		return nil
	case "":
		return fmt.Errorf("field type is required")
	default:
		return fmt.Errorf("unknown field type %q", f.Type)
	}
}

// ClampOrder returns the effective sort order for a service-contributed
// descriptor. Explicit orders below ReservedOrderCeiling are clamped up;
// missing orders produce (0, false).
//
// The caller uses the returned bool to decide whether to apply a
// secondary sort (e.g. by service ID) when no explicit order was given.
func ClampOrder(d *PanelDescriptor, isServiceContributed bool) (int, bool) {
	if d == nil || d.Order == nil {
		return 0, false
	}
	ord := *d.Order
	if isServiceContributed && ord < ReservedOrderCeiling {
		return ReservedOrderCeiling, true
	}
	return ord, true
}

// --- JSON polymorphism ---
//
// Sections marshal/unmarshal as a flat JSON object with a "kind"
// discriminator rather than nested per-kind fields. This keeps the
// on-the-wire shape consistent with the spec (a section IS a typed
// object, not a wrapper around one of several typed sub-objects).

type sectionEnvelope struct {
	Kind SectionKind `json:"kind"`
}

func (s Section) MarshalJSON() ([]byte, error) {
	switch s.Kind {
	case KindForm:
		if s.Form == nil {
			return nil, fmt.Errorf("section kind=form with nil body")
		}
		return json.Marshal(struct {
			Kind SectionKind `json:"kind"`
			*FormSection
		}{Kind: s.Kind, FormSection: s.Form})
	case KindIframe:
		if s.Iframe == nil {
			return nil, fmt.Errorf("section kind=iframe with nil body")
		}
		return json.Marshal(struct {
			Kind SectionKind `json:"kind"`
			*IframeSection
		}{Kind: s.Kind, IframeSection: s.Iframe})
	case KindStatus:
		if s.Status == nil {
			return nil, fmt.Errorf("section kind=status with nil body")
		}
		return json.Marshal(struct {
			Kind SectionKind `json:"kind"`
			*StatusSection
		}{Kind: s.Kind, StatusSection: s.Status})
	case KindLogs:
		if s.Logs == nil {
			return nil, fmt.Errorf("section kind=logs with nil body")
		}
		return json.Marshal(struct {
			Kind SectionKind `json:"kind"`
			*LogsSection
		}{Kind: s.Kind, LogsSection: s.Logs})
	default:
		return nil, fmt.Errorf("unknown section kind %q", s.Kind)
	}
}

func (s *Section) UnmarshalJSON(data []byte) error {
	var env sectionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	s.Kind = env.Kind
	switch env.Kind {
	case KindForm:
		var body FormSection
		if err := json.Unmarshal(data, &body); err != nil {
			return err
		}
		s.Form = &body
	case KindIframe:
		var body IframeSection
		if err := json.Unmarshal(data, &body); err != nil {
			return err
		}
		s.Iframe = &body
	case KindStatus:
		var body StatusSection
		if err := json.Unmarshal(data, &body); err != nil {
			return err
		}
		s.Status = &body
	case KindLogs:
		var body LogsSection
		if err := json.Unmarshal(data, &body); err != nil {
			return err
		}
		s.Logs = &body
	case "":
		return fmt.Errorf("section missing kind")
	default:
		return fmt.Errorf("unknown section kind %q", env.Kind)
	}
	return nil
}
