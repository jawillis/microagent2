package response

import (
	"encoding/hex"
	"testing"
)

func TestFlattenContent_String(t *testing.T) {
	if got := FlattenContent("hello"); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestFlattenContent_ContentPartsArray(t *testing.T) {
	in := []any{
		map[string]any{"type": "input_text", "text": "hello"},
		map[string]any{"type": "input_text", "text": "world"},
	}
	if got := FlattenContent(in); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestFlattenContent_StructuredContentParts(t *testing.T) {
	in := []ContentPart{
		{Type: "output_text", Text: "hello"},
		{Type: "output_text", Text: "world"},
	}
	if got := FlattenContent(in); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestFlattenContent_SkipsUnknownPartTypes(t *testing.T) {
	in := []any{
		map[string]any{"type": "input_text", "text": "kept"},
		map[string]any{"type": "image_url", "image_url": "x"},
		map[string]any{"type": "input_text", "text": "also kept"},
	}
	if got := FlattenContent(in); got != "kept also kept" {
		t.Fatalf("got %q", got)
	}
}

func TestStitchHash_StringEqualsContentParts(t *testing.T) {
	bare := []InputItem{
		{Role: "user", Content: "hello"},
	}
	parts := []InputItem{
		{Role: "user", Content: []any{map[string]any{"type": "input_text", "text": "hello"}}},
	}
	if StitchHash(bare) != StitchHash(parts) {
		t.Fatal("bare string and content-parts array must hash identically")
	}
}

func TestStitchHash_RoleOrderMatters(t *testing.T) {
	a := []InputItem{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hey"},
	}
	b := []InputItem{
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "hey"},
	}
	if StitchHash(a) == StitchHash(b) {
		t.Fatal("role-content swap must yield a different hash")
	}
}

func TestStitchHash_EmptyContentSkipped(t *testing.T) {
	withEmpty := []InputItem{
		{Role: "user", Content: "hi"},
		{Role: "system", Content: ""},
		{Role: "assistant", Content: "hey"},
	}
	without := []InputItem{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hey"},
	}
	if StitchHash(withEmpty) != StitchHash(without) {
		t.Fatal("items with empty flattened content must be skipped, not emitted with empty body")
	}
}

func TestStitchHash_DigestShape(t *testing.T) {
	h := StitchHash([]InputItem{{Role: "user", Content: "hi"}})
	if len(h) != 64 {
		t.Fatalf("sha256 hex expected 64 chars, got %d (%s)", len(h), h)
	}
	if _, err := hex.DecodeString(h); err != nil {
		t.Fatalf("not valid hex: %v", err)
	}
}

func TestStitchHash_MissingRoleDefaultsToUser(t *testing.T) {
	withExplicit := []InputItem{{Role: "user", Content: "hi"}}
	withMissing := []InputItem{{Role: "", Content: "hi"}}
	if StitchHash(withExplicit) != StitchHash(withMissing) {
		t.Fatal("missing role should default to user in the hash")
	}
}

func TestOutputItemToInputItem_RoundtripsThroughHash(t *testing.T) {
	out := OutputItem{
		Type: "message",
		Role: "assistant",
		Content: []ContentPart{
			{Type: "output_text", Text: "Hi Jason!"},
		},
	}
	asInput := OutputItemToInputItem(out)

	viaInputItems := []InputItem{
		{Role: "user", Content: "hello"},
		asInput,
	}
	viaBareStrings := []InputItem{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "Hi Jason!"},
	}
	if StitchHash(viaInputItems) != StitchHash(viaBareStrings) {
		t.Fatal("OutputItem→InputItem must hash identically to a bare-string equivalent")
	}
}
