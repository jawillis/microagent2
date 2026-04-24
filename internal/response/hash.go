package response

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// FlattenContent reduces a Responses-API content field to plain text.
// The field can be a bare string or an array of content parts of the form
// [{"type":"input_text","text":"..."}, ...] / [{"type":"output_text","text":"..."}].
// Text from each part is concatenated with single spaces. Unknown part types
// are skipped.
func FlattenContent(raw any) string {
	switch c := raw.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, p := range c {
			m, ok := p.(map[string]any)
			if !ok {
				continue
			}
			t, _ := m["type"].(string)
			if t != "" && t != "input_text" && t != "output_text" && t != "text" {
				continue
			}
			text, _ := m["text"].(string)
			if text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(text)
		}
		return b.String()
	case []ContentPart:
		var b strings.Builder
		for _, p := range c {
			if p.Text == "" {
				continue
			}
			if p.Type != "" && p.Type != "input_text" && p.Type != "output_text" && p.Type != "text" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

// StitchHash computes a stable SHA-256 hex digest of a conversation's
// canonical byte form. The canonical form is, in order for each item:
//
//	<lowercased-role> "\x00" <flattened-content> "\x1e"
//
// Items whose flattened content is empty are skipped (not emitted).
// The same conversation represented as bare strings vs. content-part arrays
// hashes identically; different role orderings hash differently.
func StitchHash(items []InputItem) string {
	h := sha256.New()
	for _, it := range items {
		content := FlattenContent(it.Content)
		if content == "" {
			continue
		}
		role := strings.ToLower(it.Role)
		if role == "" {
			role = "user"
		}
		h.Write([]byte(role))
		h.Write([]byte{0})
		h.Write([]byte(content))
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OutputItemToInputItem converts a stored OutputItem (assistant turn) into an
// InputItem-shaped record so it can participate in StitchHash against a
// future client-replayed conversation.
func OutputItemToInputItem(out OutputItem) InputItem {
	// Pass the structured content parts through as-is; FlattenContent knows
	// how to walk []ContentPart.
	return InputItem{
		Type:    out.Type,
		Role:    out.Role,
		Content: out.Content,
	}
}
