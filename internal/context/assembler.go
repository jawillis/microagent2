package context

import (
	"fmt"
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

	systemContent := a.systemPrompt
	if len(memories) > 0 {
		systemContent += "\n\n" + formatMemories(memories)
	}

	assembled = append(assembled, messaging.ChatMsg{
		Role:    "system",
		Content: systemContent,
	})

	assembled = append(assembled, history...)
	assembled = append(assembled, userMessage)

	return assembled
}

func formatMemories(memories []Memory) string {
	var b strings.Builder
	b.WriteString("## Relevant Context\n")
	for i, m := range memories {
		b.WriteString(fmt.Sprintf("- %s", m.Content))
		if i < len(memories)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
