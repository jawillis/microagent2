package context

import (
	"fmt"
	"sort"
	"strings"

	"microagent2/internal/messaging"
)

type Assembler struct {
	systemPrompt string
}

func NewAssembler(systemPrompt string) *Assembler {
	return &Assembler{systemPrompt: systemPrompt}
}

func (a *Assembler) Assemble(memories []Memory, history []messaging.ChatMsg, userMessage messaging.ChatMsg) []messaging.ChatMsg {
	var assembled []messaging.ChatMsg

	assembled = append(assembled, messaging.ChatMsg{
		Role:    "system",
		Content: a.systemPrompt,
	})

	assembled = append(assembled, history...)

	sorted := make([]Memory, len(memories))
	copy(sorted, memories)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})

	decorated := userMessage
	decorated.Content = formatContextBlock(sorted) + userMessage.Content
	assembled = append(assembled, decorated)

	return assembled
}

func formatContextBlock(memories []Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<context>\n")
	for _, m := range memories {
		b.WriteString(fmt.Sprintf("- %s\n", m.Content))
	}
	b.WriteString("</context>\n\n")
	return b.String()
}
