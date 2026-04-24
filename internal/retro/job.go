package retro

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jasonwillis/microagent2/internal/agent"
	appcontext "github.com/jasonwillis/microagent2/internal/context"
	"github.com/jasonwillis/microagent2/internal/messaging"
)

type JobType string

const (
	JobMemoryExtraction JobType = "memory_extraction"
	JobSkillCreation    JobType = "skill_creation"
	JobCuration         JobType = "curation"
)

type Job interface {
	Type() JobType
	Run(ctx context.Context, sessionID string, checkpoint *Checkpoint) error
}

type ExtractedMemory struct {
	Content   string   `json:"content"`
	Category  string   `json:"category"`
	KeyTerms  []string `json:"key_terms"`
	Directive string   `json:"directive"`
}

type ExtractedSkill struct {
	ProblemClass string   `json:"problem_class"`
	Approach     string   `json:"approach"`
	Outcome      string   `json:"outcome"`
	KeyTerms     []string `json:"key_terms"`
}

type MemoryExtractionJob struct {
	runtime    *agent.Runtime
	sessions   *appcontext.SessionStore
	muninn     *appcontext.MuninnClient
	logger     *slog.Logger
	checkpoint *CheckpointStore
}

func NewMemoryExtractionJob(rt *agent.Runtime, sessions *appcontext.SessionStore, muninn *appcontext.MuninnClient, logger *slog.Logger, cp *CheckpointStore) *MemoryExtractionJob {
	return &MemoryExtractionJob{
		runtime:    rt,
		sessions:   sessions,
		muninn:     muninn,
		logger:     logger,
		checkpoint: cp,
	}
}

func (j *MemoryExtractionJob) Type() JobType { return JobMemoryExtraction }

func (j *MemoryExtractionJob) Run(ctx context.Context, sessionID string, cp *Checkpoint) error {
	history, err := j.sessions.GetHistory(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get history: %w", err)
	}
	if len(history) == 0 {
		return nil
	}

	startIdx := 0
	if cp != nil {
		startIdx = cp.ProcessedTurns
	}
	if startIdx >= len(history) {
		return nil
	}

	unprocessed := history[startIdx:]
	prompt := buildMemoryExtractionPrompt(unprocessed)

	slotID, err := j.runtime.RequestSlot(ctx)
	if err != nil {
		return fmt.Errorf("request slot: %w", err)
	}
	defer j.runtime.ReleaseSlot(ctx)

	result, err := j.runtime.Execute(ctx, prompt, nil)
	if err == messaging.ErrPreempted {
		j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{
			ProcessedTurns: startIdx + countProcessedFromProgress(j.runtime.GetProgressLog(), unprocessed),
		})
		return err
	}
	if err != nil {
		return fmt.Errorf("execute (slot %d): %w", slotID, err)
	}

	memories, err := parseMemories(result)
	if err != nil {
		j.logger.Warn("failed to parse memory extraction output", "error", err, "session", sessionID)
		return nil
	}

	for _, mem := range memories {
		if err := j.muninn.Store(ctx, mem.Content, mem.Category, mem.KeyTerms); err != nil {
			j.logger.Error("failed to store memory", "error", err, "category", mem.Category)
		}
	}

	j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{ProcessedTurns: len(history)})
	j.logger.Info("memory extraction complete", "session", sessionID, "memories_stored", len(memories))
	return nil
}

type SkillCreationJob struct {
	runtime    *agent.Runtime
	sessions   *appcontext.SessionStore
	muninn     *appcontext.MuninnClient
	logger     *slog.Logger
	checkpoint *CheckpointStore
}

func NewSkillCreationJob(rt *agent.Runtime, sessions *appcontext.SessionStore, muninn *appcontext.MuninnClient, logger *slog.Logger, cp *CheckpointStore) *SkillCreationJob {
	return &SkillCreationJob{
		runtime:    rt,
		sessions:   sessions,
		muninn:     muninn,
		logger:     logger,
		checkpoint: cp,
	}
}

func (j *SkillCreationJob) Type() JobType { return JobSkillCreation }

func (j *SkillCreationJob) Run(ctx context.Context, sessionID string, cp *Checkpoint) error {
	history, err := j.sessions.GetHistory(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get history: %w", err)
	}
	if len(history) < 4 {
		return nil
	}

	startIdx := 0
	if cp != nil {
		startIdx = cp.ProcessedTurns
	}
	if startIdx >= len(history) {
		return nil
	}

	unprocessed := history[startIdx:]
	prompt := buildSkillCreationPrompt(unprocessed)

	slotID, err := j.runtime.RequestSlot(ctx)
	if err != nil {
		return fmt.Errorf("request slot: %w", err)
	}
	defer j.runtime.ReleaseSlot(ctx)

	result, err := j.runtime.Execute(ctx, prompt, nil)
	if err == messaging.ErrPreempted {
		j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{
			ProcessedTurns: startIdx + countProcessedFromProgress(j.runtime.GetProgressLog(), unprocessed),
		})
		return err
	}
	if err != nil {
		return fmt.Errorf("execute (slot %d): %w", slotID, err)
	}

	skills, err := parseSkills(result)
	if err != nil {
		j.logger.Warn("failed to parse skill creation output", "error", err, "session", sessionID)
		return nil
	}

	for _, skill := range skills {
		existing, _ := j.muninn.Recall(ctx, skill.ProblemClass, 3)
		if isDuplicateSkill(skill, existing) {
			j.logger.Debug("skipping duplicate skill", "problem_class", skill.ProblemClass)
			continue
		}

		content := fmt.Sprintf("Problem: %s\nApproach: %s\nOutcome: %s", skill.ProblemClass, skill.Approach, skill.Outcome)
		if err := j.muninn.Store(ctx, content, "skill", skill.KeyTerms); err != nil {
			j.logger.Error("failed to store skill", "error", err, "problem_class", skill.ProblemClass)
		}
	}

	j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{ProcessedTurns: len(history)})
	j.logger.Info("skill creation complete", "session", sessionID, "skills_found", len(skills))
	return nil
}

type CurationJob struct {
	runtime *agent.Runtime
	muninn  *appcontext.MuninnClient
	logger  *slog.Logger
}

func NewCurationJob(rt *agent.Runtime, muninn *appcontext.MuninnClient, logger *slog.Logger) *CurationJob {
	return &CurationJob{
		runtime: rt,
		muninn:  muninn,
		logger:  logger,
	}
}

func (j *CurationJob) Type() JobType { return JobCuration }

func (j *CurationJob) Run(ctx context.Context, sessionID string, _ *Checkpoint) error {
	categories := []string{"preference", "fact", "context", "skill"}

	for _, category := range categories {
		entries, err := j.muninn.Recall(ctx, category, 50)
		if err != nil {
			j.logger.Warn("failed to recall entries for curation", "category", category, "error", err)
			continue
		}
		if len(entries) < 2 {
			continue
		}

		prompt := buildCurationPrompt(category, entries)

		if _, err := j.runtime.RequestSlot(ctx); err != nil {
			return fmt.Errorf("request slot: %w", err)
		}

		result, err := j.runtime.Execute(ctx, prompt, nil)
		_ = j.runtime.ReleaseSlot(ctx)

		if err == messaging.ErrPreempted {
			return err
		}
		if err != nil {
			j.logger.Error("curation execution failed", "category", category, "error", err)
			continue
		}

		actions, err := parseCurationActions(result)
		if err != nil {
			j.logger.Warn("failed to parse curation output", "category", category, "error", err)
			continue
		}

		j.logger.Info("curation complete", "category", category, "actions", len(actions))
	}

	return nil
}

func buildMemoryExtractionPrompt(history []messaging.ChatMsg) []messaging.ChatMsg {
	var sb strings.Builder
	for _, msg := range history {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	return []messaging.ChatMsg{
		{Role: "system", Content: `You are a memory extraction agent. Analyze the conversation and extract facts, preferences, and contextual information worth remembering long-term.

Output a JSON array of memory objects. Each object must have:
- "content": the memory phrased as an actionable directive
- "category": one of "preference", "fact", "context"
- "key_terms": array of terms for embedding similarity search
- "directive": how the agent should use this information

If nothing is worth storing, output an empty array: []

Output ONLY valid JSON, no other text.`},
		{Role: "user", Content: fmt.Sprintf("Analyze this conversation for memorable information:\n\n%s", sb.String())},
	}
}

func buildSkillCreationPrompt(history []messaging.ChatMsg) []messaging.ChatMsg {
	var sb strings.Builder
	for _, msg := range history {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	return []messaging.ChatMsg{
		{Role: "system", Content: `You are a skill extraction agent. Analyze the conversation for reusable problem-solving patterns.

A skill captures: a problem class, the approach taken, and the outcome.

Output a JSON array of skill objects. Each object must have:
- "problem_class": what type of problem was solved
- "approach": the method or technique used
- "outcome": the result
- "key_terms": array of terms for retrieval

If no reusable patterns found, output an empty array: []

Output ONLY valid JSON, no other text.`},
		{Role: "user", Content: fmt.Sprintf("Analyze this conversation for reusable problem-solving patterns:\n\n%s", sb.String())},
	}
}

func buildCurationPrompt(category string, entries []appcontext.Memory) []messaging.ChatMsg {
	var sb strings.Builder
	for i, e := range entries {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i, e.Content))
	}

	return []messaging.ChatMsg{
		{Role: "system", Content: fmt.Sprintf(`You are a memory curation agent reviewing the "%s" category.

Find duplicates, contradictions, and stale entries.

Output a JSON array of action objects. Each object must have:
- "action": one of "merge", "remove", "keep"
- "indices": array of entry indices this action applies to
- "reason": brief explanation
- "merged_content": (only for "merge") the merged content

If no actions needed, output an empty array: []

Output ONLY valid JSON, no other text.`, category)},
		{Role: "user", Content: fmt.Sprintf("Review these %s entries:\n\n%s", category, sb.String())},
	}
}

func parseMemories(llmOutput string) ([]ExtractedMemory, error) {
	llmOutput = strings.TrimSpace(llmOutput)
	llmOutput = trimJSONFences(llmOutput)

	var memories []ExtractedMemory
	if err := json.Unmarshal([]byte(llmOutput), &memories); err != nil {
		return nil, fmt.Errorf("parse memories: %w", err)
	}
	return memories, nil
}

func parseSkills(llmOutput string) ([]ExtractedSkill, error) {
	llmOutput = strings.TrimSpace(llmOutput)
	llmOutput = trimJSONFences(llmOutput)

	var skills []ExtractedSkill
	if err := json.Unmarshal([]byte(llmOutput), &skills); err != nil {
		return nil, fmt.Errorf("parse skills: %w", err)
	}
	return skills, nil
}

type curationAction struct {
	Action        string `json:"action"`
	Indices       []int  `json:"indices"`
	Reason        string `json:"reason"`
	MergedContent string `json:"merged_content,omitempty"`
}

func parseCurationActions(llmOutput string) ([]curationAction, error) {
	llmOutput = strings.TrimSpace(llmOutput)
	llmOutput = trimJSONFences(llmOutput)

	var actions []curationAction
	if err := json.Unmarshal([]byte(llmOutput), &actions); err != nil {
		return nil, fmt.Errorf("parse curation actions: %w", err)
	}
	return actions, nil
}

func trimJSONFences(s string) string {
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

func isDuplicateSkill(skill ExtractedSkill, existing []appcontext.Memory) bool {
	for _, mem := range existing {
		if mem.Score > 0.85 && strings.Contains(strings.ToLower(mem.Content), strings.ToLower(skill.ProblemClass)) {
			return true
		}
	}
	return false
}

func countProcessedFromProgress(progressLog []string, history []messaging.ChatMsg) int {
	output := strings.Join(progressLog, "")
	count := 0
	for i, msg := range history {
		if strings.Contains(output, msg.Content[:min(len(msg.Content), 50)]) {
			count = i + 1
		}
	}
	return count
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
