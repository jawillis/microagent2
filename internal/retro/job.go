package retro

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"microagent2/internal/agent"
	appcontext "microagent2/internal/context"
	"microagent2/internal/messaging"
	"microagent2/internal/response"
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

// Muninn's memory_type enum (docs/feature-reference.md:295-308).
var validMemoryTypes = map[string]struct{}{
	"fact":        {},
	"decision":    {},
	"observation": {},
	"preference":  {},
	"issue":       {},
	"task":        {},
	"procedure":   {},
	"event":       {},
	"goal":        {},
	"constraint":  {},
	"identity":    {},
	"reference":   {},
}

type ExtractedMemory struct {
	Concept    string   `json:"concept"`
	Content    string   `json:"content"`
	Summary    string   `json:"summary"`
	Tags       []string `json:"tags"`
	MemoryType string   `json:"memory_type"`
	Confidence float64  `json:"confidence"`
}

type ExtractedSkill struct {
	Concept      string   `json:"concept"`
	ProblemClass string   `json:"problem_class"`
	Approach     string   `json:"approach"`
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Tags         []string `json:"tags"`
	Confidence   float64  `json:"confidence"`
}

type MemoryExtractionJob struct {
	runtime    *agent.Runtime
	responses  *response.Store
	muninn     *appcontext.MuninnClient
	logger     *slog.Logger
	checkpoint *CheckpointStore
}

func NewMemoryExtractionJob(rt *agent.Runtime, responses *response.Store, muninn *appcontext.MuninnClient, logger *slog.Logger, cp *CheckpointStore) *MemoryExtractionJob {
	return &MemoryExtractionJob{
		runtime:    rt,
		responses:  responses,
		muninn:     muninn,
		logger:     logger,
		checkpoint: cp,
	}
}

func (j *MemoryExtractionJob) Type() JobType { return JobMemoryExtraction }

func (j *MemoryExtractionJob) Run(ctx context.Context, sessionID string, cp *Checkpoint) error {
	history, err := j.responses.GetSessionMessages(ctx, sessionID)
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
		spec := memoryToStoredSpec(mem)
		if spec.Concept == "" || spec.Content == "" {
			j.logger.Warn("skipping memory with empty concept or content", "concept", spec.Concept)
			continue
		}
		if err := j.muninn.Store(ctx, spec); err != nil {
			j.logger.Error("failed to store memory", "error", err, "concept", spec.Concept)
		}
	}

	j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{ProcessedTurns: len(history)})
	j.logger.Info("memory extraction complete", "session", sessionID, "memories_stored", len(memories))
	return nil
}

type SkillCreationJob struct {
	runtime           *agent.Runtime
	responses         *response.Store
	muninn            *appcontext.MuninnClient
	logger            *slog.Logger
	checkpoint        *CheckpointStore
	minHistoryTurns   int
	skillDupThreshold float64
}

func NewSkillCreationJob(rt *agent.Runtime, responses *response.Store, muninn *appcontext.MuninnClient, logger *slog.Logger, cp *CheckpointStore, minHistoryTurns int, skillDupThreshold float64) *SkillCreationJob {
	return &SkillCreationJob{
		runtime:           rt,
		responses:         responses,
		muninn:            muninn,
		logger:            logger,
		checkpoint:        cp,
		minHistoryTurns:   minHistoryTurns,
		skillDupThreshold: skillDupThreshold,
	}
}

func (j *SkillCreationJob) Type() JobType { return JobSkillCreation }

func (j *SkillCreationJob) Run(ctx context.Context, sessionID string, cp *Checkpoint) error {
	history, err := j.responses.GetSessionMessages(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get history: %w", err)
	}
	if len(history) < j.minHistoryTurns {
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
		if isDuplicateSkill(skill, existing, j.skillDupThreshold) {
			j.logger.Debug("skipping duplicate skill", "problem_class", skill.ProblemClass)
			continue
		}

		spec := skillToStoredSpec(skill)
		if spec.Concept == "" || spec.Content == "" {
			j.logger.Warn("skipping skill with empty concept or content", "problem_class", skill.ProblemClass)
			continue
		}
		if err := j.muninn.Store(ctx, spec); err != nil {
			j.logger.Error("failed to store skill", "error", err, "concept", spec.Concept)
		}
	}

	j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{ProcessedTurns: len(history)})
	j.logger.Info("skill creation complete", "session", sessionID, "skills_found", len(skills))
	return nil
}

// curationMuninn is the subset of MuninnClient used by the curation job.
// Declared locally so tests can substitute a fake.
type curationMuninn interface {
	Recall(ctx context.Context, query string, limit int) ([]appcontext.Memory, error)
	Consolidate(ctx context.Context, ids []string, mergedContent string) (string, error)
	Evolve(ctx context.Context, id, newContent, newSummary string) (string, error)
	Delete(ctx context.Context, id string) error
}

type CurationJob struct {
	runtime    *agent.Runtime
	muninn     curationMuninn
	logger     *slog.Logger
	categories []string
}

func NewCurationJob(rt *agent.Runtime, muninn *appcontext.MuninnClient, logger *slog.Logger, categories []string) *CurationJob {
	return &CurationJob{
		runtime:    rt,
		muninn:     muninn,
		logger:     logger,
		categories: categories,
	}
}

func (j *CurationJob) Type() JobType { return JobCuration }

func (j *CurationJob) Run(ctx context.Context, sessionID string, _ *Checkpoint) error {
	for _, category := range j.categories {
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

		summary := j.executeCurationActions(ctx, category, entries, actions)
		j.logger.Info("retro_curation_complete",
			"category", category,
			"merged", summary.merged,
			"evolved", summary.evolved,
			"deleted", summary.deleted,
			"skipped", summary.skipped,
		)
	}

	return nil
}

type curationSummary struct {
	merged, evolved, deleted, skipped int
}

func (j *CurationJob) executeCurationActions(ctx context.Context, category string, entries []appcontext.Memory, actions []curationAction) curationSummary {
	var s curationSummary

	for _, action := range actions {
		logFields := []any{
			"category", category,
			"action", action.Action,
			"indices", action.Indices,
			"reason", action.Reason,
		}

		if !validateCurationIndices(action, len(entries)) {
			j.logger.Warn("retro_curation_action_invalid", logFields...)
			s.skipped++
			continue
		}

		engramIDs, ok := entriesToIDs(entries, action.Indices)
		if !ok {
			j.logger.Warn("retro_curation_action_missing_ids", logFields...)
			s.skipped++
			continue
		}

		j.logger.Info("retro_curation_action", logFields...)

		switch action.Action {
		case "merge":
			if _, err := j.muninn.Consolidate(ctx, engramIDs, action.MergedContent); err != nil {
				j.logger.Warn("retro_curation_action_failed", append(logFields, "error", err.Error())...)
				continue
			}
			s.merged++
		case "evolve":
			if _, err := j.muninn.Evolve(ctx, engramIDs[0], action.MergedContent, ""); err != nil {
				j.logger.Warn("retro_curation_action_failed", append(logFields, "error", err.Error())...)
				continue
			}
			s.evolved++
		case "delete":
			if err := j.muninn.Delete(ctx, engramIDs[0]); err != nil {
				j.logger.Warn("retro_curation_action_failed", append(logFields, "error", err.Error())...)
				continue
			}
			s.deleted++
		default:
			j.logger.Warn("retro_curation_unknown_action", logFields...)
			s.skipped++
		}
	}

	return s
}

func validateCurationIndices(action curationAction, n int) bool {
	switch action.Action {
	case "merge":
		if len(action.Indices) < 2 || strings.TrimSpace(action.MergedContent) == "" {
			return false
		}
	case "evolve":
		if len(action.Indices) != 1 || strings.TrimSpace(action.MergedContent) == "" {
			return false
		}
	case "delete":
		if len(action.Indices) != 1 {
			return false
		}
	default:
		return true
	}
	for _, idx := range action.Indices {
		if idx < 0 || idx >= n {
			return false
		}
	}
	return true
}

func entriesToIDs(entries []appcontext.Memory, indices []int) ([]string, bool) {
	ids := make([]string, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(entries) {
			return nil, false
		}
		id := entries[idx].ID
		if id == "" {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func memoryToStoredSpec(mem ExtractedMemory) appcontext.StoredMemory {
	spec := appcontext.StoredMemory{
		Concept: strings.TrimSpace(mem.Concept),
		Content: strings.TrimSpace(mem.Content),
		Summary: strings.TrimSpace(mem.Summary),
		Tags:    mem.Tags,
	}
	if _, ok := validMemoryTypes[mem.MemoryType]; ok {
		spec.MemoryType = mem.MemoryType
	}
	if mem.Confidence > 0.0 && mem.Confidence <= 1.0 {
		spec.Confidence = mem.Confidence
	}
	return spec
}

func skillToStoredSpec(skill ExtractedSkill) appcontext.StoredMemory {
	concept := strings.TrimSpace(skill.Concept)
	if concept == "" {
		concept = strings.TrimSpace(skill.ProblemClass)
	}
	content := fmt.Sprintf("Problem: %s\nApproach: %s\nOutcome: %s",
		strings.TrimSpace(skill.ProblemClass),
		strings.TrimSpace(skill.Approach),
		strings.TrimSpace(skill.Outcome),
	)
	spec := appcontext.StoredMemory{
		Concept:    concept,
		Content:    content,
		Summary:    strings.TrimSpace(skill.Summary),
		Tags:       skill.Tags,
		MemoryType: "procedure",
	}
	if skill.Confidence > 0.0 && skill.Confidence <= 1.0 {
		spec.Confidence = skill.Confidence
	}
	return spec
}

func buildMemoryExtractionPrompt(history []messaging.ChatMsg) []messaging.ChatMsg {
	var sb strings.Builder
	for _, msg := range history {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	return []messaging.ChatMsg{
		{Role: "system", Content: `You are a memory extraction agent. Analyze the conversation and extract facts, preferences, and contextual information worth remembering long-term.

Output a JSON array of memory objects. Each object must have:

- "concept": a SPECIFIC headline sentence describing what this memory is about. This is the highest-weight full-text-search field (3x). It MUST be a specific sentence, NOT a category label.
    GOOD: "Jason prefers dark roast coffee", "Jason is allergic to shellfish", "The user works remotely from Denver"
    BAD:  "preference", "fact", "food_preference", "user_profile"

- "content": the full declarative statement of the memory.
    GOOD: "Jason drinks dark roast coffee every morning because light roasts taste sour to him."

- "summary": a one-line restatement of the memory (<= 140 characters). Used as the human-readable rendering of the memory.

- "tags": 3-8 short words a user is LIKELY TO TYPE in a query where this memory should be recalled. These drive keyword-search (2x weight). They must be natural query words, NOT semantic categories.
    GOOD (for a coffee preference): ["coffee","caffeine","morning","drink","beans","roast"]
    BAD  (for a coffee preference): ["beverage_preference","caffeine_intake","user_preference"]

- "memory_type": one of exactly these 12 values:
    "fact", "decision", "observation", "preference", "issue", "task",
    "procedure", "event", "goal", "constraint", "identity", "reference"
  If unsure, pick the closest match or omit the field.

- "confidence": a float between 0.0 and 1.0 reflecting how certain you are about this extraction. Strong direct statements from the user -> close to 1.0. Inferences or ambiguous signals -> lower values.

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
		{Role: "system", Content: `You are a skill extraction agent. Analyze the conversation for reusable problem-solving patterns (procedures).

A skill captures: a problem class, the approach taken, and the outcome.

Output a JSON array of skill objects. Each object must have:

- "concept": a SPECIFIC headline describing the approach. This is the highest-weight FTS field (3x). Must be a specific sentence, NOT a category label.
    GOOD: "Approach for diagnosing flaky CI tests by isolating shared fixtures"
    BAD:  "skill", "debugging", "testing"

- "problem_class": what type of problem was solved.
- "approach": the method or technique used.
- "outcome": the result.

- "summary": a one-line restatement (<= 140 characters) combining problem, approach, and outcome.

- "tags": 3-8 words a user is LIKELY TO TYPE when they hit this class of problem and would benefit from this skill. These drive keyword-search (2x weight). Use natural query words.
    GOOD (for flaky-tests skill): ["flaky","tests","ci","fixtures","intermittent","shared state"]
    BAD  (for flaky-tests skill): ["test_reliability","fixture_isolation"]

- "confidence": a float between 0.0 and 1.0 reflecting how well-supported this pattern is by the conversation.

(memory_type is implicitly "procedure" for skills; you do not need to emit it.)

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
- "action": one of exactly "merge", "evolve", "delete"
    * merge: combine 2+ entries into one new entry. Requires >= 2 indices and a merged_content.
    * evolve: refine a single entry with a better-phrased content. Requires exactly 1 index and merged_content.
    * delete: remove a single stale or wrong entry. Requires exactly 1 index.
- "indices": array of entry indices this action applies to
- "reason": brief explanation
- "merged_content": (required for merge and evolve) the new content

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

func isDuplicateSkill(skill ExtractedSkill, existing []appcontext.Memory, threshold float64) bool {
	for _, mem := range existing {
		if mem.Score > threshold && strings.Contains(strings.ToLower(mem.Content), strings.ToLower(skill.ProblemClass)) {
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
