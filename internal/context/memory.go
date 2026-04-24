package context

// Memory is the consumer-facing projection the assembler works with. It
// deliberately mirrors memoryclient.MemorySummary's shape at the fields used
// for context assembly, so the translation at the boundary is a straight copy.
type Memory struct {
	ID      string
	Content string
	Score   float64
	Tags    []string
}
