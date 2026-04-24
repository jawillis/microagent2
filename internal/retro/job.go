package retro

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"microagent2/internal/agent"
	"microagent2/internal/memoryclient"
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

// ExtractedMemory is the LLM-extracted shape parsed from the extraction prompt.
// MemoryType / Concept / Summary are retained on the input shape for backward
// compatibility with the extraction prompt, but the retain path now passes
// only Content + Tags + Metadata (confidence/provenance) to memory-service.
type ExtractedMemory struct {
	Concept      string   `json:"concept"`
	Content      string   `json:"content"`
	Summary      string   `json:"summary"`
	Tags         []string `json:"tags"`
	MemoryType   string   `json:"memory_type"`
	Confidence   float64  `json:"confidence"`
	IsCorrection bool     `json:"is_correction,omitempty"`
}

// ExtractedSkill is the LLM-extracted shape parsed from the skill-extraction prompt.
type ExtractedSkill struct {
	Concept      string   `json:"concept"`
	ProblemClass string   `json:"problem_class"`
	Approach     string   `json:"approach"`
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Tags         []string `json:"tags"`
	Confidence   float64  `json:"confidence"`
}

// --- MemoryExtractionJob ---

type MemoryExtractionJob struct {
	runtime    *agent.Runtime
	responses  *response.Store
	memory     *memoryclient.Client
	logger     *slog.Logger
	checkpoint *CheckpointStore
}

func NewMemoryExtractionJob(rt *agent.Runtime, responses *response.Store, mc *memoryclient.Client, logger *slog.Logger, cp *CheckpointStore) *MemoryExtractionJob {
	return &MemoryExtractionJob{runtime: rt, responses: responses, memory: mc, logger: logger, checkpoint: cp}
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

	result, _, err := j.runtime.Execute(ctx, prompt, nil, nil, nil)
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

	stored := 0
	for _, mem := range memories {
		if strings.TrimSpace(mem.Content) == "" {
			continue
		}
		req := buildExtractRetainRequest(mem)
		if _, err := j.memory.Retain(ctx, req); err != nil {
			j.logger.Error("memory_retain_failed", "error", err, "content_prefix", truncate(mem.Content, 40))
			continue
		}
		stored++
	}

	j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{ProcessedTurns: len(history)})
	j.logger.Info("memory_extraction_complete",
		"session", sessionID,
		"memories_extracted", len(memories),
		"memories_stored", stored,
	)
	return nil
}

// --- SkillCreationJob ---

type SkillCreationJob struct {
	runtime           *agent.Runtime
	responses         *response.Store
	memory            *memoryclient.Client
	logger            *slog.Logger
	checkpoint        *CheckpointStore
	minHistoryTurns   int
	skillDupThreshold float64
}

func NewSkillCreationJob(rt *agent.Runtime, responses *response.Store, mc *memoryclient.Client, logger *slog.Logger, cp *CheckpointStore, minHistoryTurns int, skillDupThreshold float64) *SkillCreationJob {
	return &SkillCreationJob{
		runtime:           rt,
		responses:         responses,
		memory:            mc,
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

	result, _, err := j.runtime.Execute(ctx, prompt, nil, nil, nil)
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

	stored := 0
	for _, skill := range skills {
		// Duplicate check via memory-service recall on the problem_class with
		// the skill tag scoping.
		existing, err := j.memory.Recall(ctx, memoryclient.RecallRequest{
			Query: skill.ProblemClass,
			Limit: 3,
			Tags:  []string{"skill"},
		})
		if err == nil && isDuplicateSkill(skill, existing.Memories, j.skillDupThreshold) {
			j.logger.Debug("skipping duplicate skill", "problem_class", skill.ProblemClass)
			continue
		}

		req := buildSkillRetainRequest(skill)
		if req.Content == "" {
			continue
		}
		if _, err := j.memory.Retain(ctx, req); err != nil {
			j.logger.Error("skill_retain_failed", "error", err, "problem_class", skill.ProblemClass)
			continue
		}
		stored++
	}

	j.checkpoint.Save(sessionID, j.Type(), &Checkpoint{ProcessedTurns: len(history)})
	j.logger.Info("skill_creation_complete",
		"session", sessionID,
		"skills_found", len(skills),
		"skills_stored", stored,
	)
	return nil
}

// --- CurationJob ---

// CurationJob is now a scheduling shim: it triggers Hindsight's observation
// consolidation via memory-service on each run, and logs pre/post state.
// Explicit merge/evolve/delete actions are gone — observation refinement
// happens inside Hindsight driven by the bank's observations_mission.
//
// Mental Model refresh is handled by a separate CurationJob cadence; the
// `recallLimit` field is retained for forward compatibility with that work
// but is no longer used today.
type CurationJob struct {
	runtime     *agent.Runtime
	memory      *memoryclient.Client
	logger      *slog.Logger
	categories  []string
	recallLimit int
}

func NewCurationJob(rt *agent.Runtime, mc *memoryclient.Client, logger *slog.Logger, categories []string, recallLimit int) *CurationJob {
	return &CurationJob{
		runtime:     rt,
		memory:      mc,
		logger:      logger,
		categories:  categories,
		recallLimit: recallLimit,
	}
}

func (j *CurationJob) Type() JobType { return JobCuration }

// Run triggers a consolidation reflect on each configured category so the
// Hindsight bank's observations_mission can incorporate recent retains.
// The run is observational — it reads recall counts pre and post to verify
// something happened, and logs the outcome.
func (j *CurationJob) Run(ctx context.Context, sessionID string, _ *Checkpoint) error {
	start := time.Now()
	pre := j.categoryCounts(ctx)

	// A Reflect call with a broad query triggers Hindsight to exercise its
	// consolidation and observation machinery. It's the lightest "nudge" we
	// can make from a consumer without a dedicated trigger endpoint.
	_, err := j.memory.Reflect(ctx, memoryclient.ReflectRequest{
		Query: "Summarize recent developments across observations.",
	})
	if err != nil {
		j.logger.Warn("retro_curation_reflect_failed", "error", err.Error())
	}

	post := j.categoryCounts(ctx)
	j.logger.Info("retro_curation_cycle",
		"session", sessionID,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"categories", j.categories,
		"counts_before", pre,
		"counts_after", post,
	)
	return nil
}

// categoryCounts queries recall for each configured category tag and returns
// the count of memories returned. It's an observability helper, not a
// correctness-critical metric.
func (j *CurationJob) categoryCounts(ctx context.Context) map[string]int {
	limit := j.recallLimit
	if limit <= 0 {
		limit = 15
	}
	counts := map[string]int{}
	for _, cat := range j.categories {
		resp, err := j.memory.Recall(ctx, memoryclient.RecallRequest{
			Query: cat,
			Tags:  []string{cat},
			Limit: limit,
		})
		if err != nil {
			counts[cat] = -1
			continue
		}
		counts[cat] = len(resp.Memories)
	}
	return counts
}

// --- retain-request construction ---

// buildExtractRetainRequest turns an ExtractedMemory into a memoryclient
// RetainRequest. Confidence goes into metadata as a string; provenance is
// "explicit" by default (direct statement from the user turn).
func buildExtractRetainRequest(mem ExtractedMemory) memoryclient.RetainRequest {
	metadata := map[string]string{
		"provenance": "explicit",
	}
	if mem.Confidence > 0 && mem.Confidence <= 1.0 {
		metadata["confidence"] = fmt.Sprintf("%.2f", mem.Confidence)
	}
	// Surface the extractor's memory_type classification in metadata for
	// future consumers; Hindsight also classifies internally.
	if mem.MemoryType != "" {
		metadata["memory_type_hint"] = mem.MemoryType
	}
	if mem.IsCorrection {
		// Corrections are signaled via metadata, not via tags. A
		// "corrections" tag would put the new fact in a different
		// Hindsight consolidation scope than the original observation
		// (all_strict tag matching), preventing the UPDATE that should
		// supersede the old claim. See reference_hindsight_tag_scope.md.
		metadata["is_correction"] = "true"
	}
	tags := stripCorrectionsTag(uniqueTags(mem.Tags))
	return memoryclient.RetainRequest{
		Content:  strings.TrimSpace(mem.Content),
		Tags:     tags,
		Metadata: metadata,
		// observation_scopes omitted → Hindsight default "combined": one
		// consolidation pass using all tags together. "per_tag" duplicates
		// the same memory into one observation per tag, which is useful when
		// topics need independently-decaying observation sets but misleading
		// when a memory simply spans multiple topics (e.g., a car fact
		// tagged identity+technical+home).
	}
}

// stripCorrectionsTag removes the "corrections" tag defensively if the
// extractor LLM emits it despite prompt guidance. See
// reference_hindsight_tag_scope.md for why this tag breaks consolidation.
func stripCorrectionsTag(tags []string) []string {
	out := tags[:0]
	for _, t := range tags {
		if strings.EqualFold(t, "corrections") || strings.EqualFold(t, "correction") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// buildSkillRetainRequest turns an ExtractedSkill into a retain request,
// always tagged "skill" with provenance=implicit (skills are observed patterns,
// not user-stated).
func buildSkillRetainRequest(skill ExtractedSkill) memoryclient.RetainRequest {
	concept := strings.TrimSpace(skill.Concept)
	if concept == "" {
		concept = strings.TrimSpace(skill.ProblemClass)
	}
	content := strings.TrimSpace(concept)
	if content == "" {
		return memoryclient.RetainRequest{}
	}
	// Compose content with problem → approach → outcome if any are present.
	parts := []string{content}
	if p := strings.TrimSpace(skill.ProblemClass); p != "" {
		parts = append(parts, "Problem: "+p)
	}
	if a := strings.TrimSpace(skill.Approach); a != "" {
		parts = append(parts, "Approach: "+a)
	}
	if o := strings.TrimSpace(skill.Outcome); o != "" {
		parts = append(parts, "Outcome: "+o)
	}
	metadata := map[string]string{"provenance": "implicit"}
	if skill.Confidence > 0 && skill.Confidence <= 1.0 {
		metadata["confidence"] = fmt.Sprintf("%.2f", skill.Confidence)
	}
	tags := uniqueTags(append([]string{"skill"}, skill.Tags...))
	return memoryclient.RetainRequest{
		Content:  strings.Join(parts, "\n"),
		Tags:     tags,
		Metadata: metadata,
	}
}

// --- prompt builders ---

func buildMemoryExtractionPrompt(history []messaging.ChatMsg) []messaging.ChatMsg {
	var sb strings.Builder
	for _, msg := range history {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}
	return []messaging.ChatMsg{
		{Role: "system", Content: extractionPrompt},
		{Role: "user", Content: fmt.Sprintf("Analyze this conversation for memorable information:\n\n%s", sb.String())},
	}
}

func buildSkillCreationPrompt(history []messaging.ChatMsg) []messaging.ChatMsg {
	var sb strings.Builder
	for _, msg := range history {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}
	return []messaging.ChatMsg{
		{Role: "system", Content: skillPrompt},
		{Role: "user", Content: fmt.Sprintf("Analyze this conversation for reusable problem-solving patterns:\n\n%s", sb.String())},
	}
}

const extractionPrompt = `You are a memory extraction agent. Analyze the conversation and extract facts, preferences, and contextual information worth remembering long-term.

Output a JSON array of memory objects. Each object must have:

- "concept": a SPECIFIC headline sentence describing what this memory is about.
- "content": the full declarative statement of the memory.
- "summary": a one-line restatement of the memory (<= 140 characters).
- "tags": 3-8 short words a user is LIKELY TO TYPE in a query where this memory should be recalled. Include at least one tag from this set when appropriate: identity, preferences, technical, home, ephemera. Do NOT tag corrections with "corrections" — a correction should carry the SAME tags as the subject it corrects (e.g. ["preferences","coffee"]) so Hindsight's consolidation can find and UPDATE the original observation rather than creating a duplicate. Set "is_correction": true in the output to flag corrections via metadata instead.
- "is_correction": true when this memory revises or supersedes a prior claim ("actually", "I was wrong", "I changed my mind"); false or omitted otherwise.
- "memory_type": one of "fact", "decision", "observation", "preference", "issue", "task", "procedure", "event", "goal", "constraint", "identity", "reference". Omit if unsure.
- "confidence": a float between 0.0 and 1.0 reflecting extraction certainty.

If nothing is worth storing, output an empty array: []

Output ONLY valid JSON, no other text.`

const skillPrompt = `You are a skill extraction agent. Analyze the conversation for reusable problem-solving patterns.

Output a JSON array of skill objects. Each object must have:

- "concept": a SPECIFIC headline describing the approach.
- "problem_class": what type of problem was solved.
- "approach": the method or technique used.
- "outcome": the result.
- "summary": a one-line restatement (<= 140 characters) combining problem, approach, and outcome.
- "tags": 3-8 words a user is LIKELY TO TYPE when they hit this class of problem.
- "confidence": a float between 0.0 and 1.0 reflecting pattern strength.

If no reusable patterns found, output an empty array: []

Output ONLY valid JSON, no other text.`

// --- parsing helpers ---

func parseMemories(llmOutput string) ([]ExtractedMemory, error) {
	s := trimJSONFences(llmOutput)
	var memories []ExtractedMemory
	if err := json.Unmarshal([]byte(s), &memories); err != nil {
		return nil, err
	}
	return memories, nil
}

func parseSkills(llmOutput string) ([]ExtractedSkill, error) {
	s := trimJSONFences(llmOutput)
	var skills []ExtractedSkill
	if err := json.Unmarshal([]byte(s), &skills); err != nil {
		return nil, err
	}
	return skills, nil
}

func trimJSONFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// isDuplicateSkill returns true if an existing skill memory has substantial
// token overlap with the extracted skill. This is a fuzzy local check that
// runs before the retain call so we avoid trivial duplicates.
func isDuplicateSkill(skill ExtractedSkill, existing []memoryclient.MemorySummary, threshold float64) bool {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.85
	}
	for _, m := range existing {
		if jaccard(skill.ProblemClass, m.Content) >= threshold {
			return true
		}
	}
	return false
}

func jaccard(a, b string) float64 {
	aWords := toWordSet(a)
	bWords := toWordSet(b)
	if len(aWords) == 0 || len(bWords) == 0 {
		return 0
	}
	inter := 0
	for w := range aWords {
		if bWords[w] {
			inter++
		}
	}
	union := len(aWords) + len(bWords) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func toWordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.TrimFunc(w, func(r rune) bool { return !isAlphaNum(r) })
		if w != "" {
			out[w] = true
		}
	}
	return out
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func countProcessedFromProgress(progressLog []string, history []messaging.ChatMsg) int {
	// Best-effort: if the progress log is empty, we processed nothing.
	if len(progressLog) == 0 {
		return 0
	}
	// Count the number of complete user/assistant role markers seen in the
	// partial output. This mirrors the old implementation's signaling.
	markers := 0
	for _, tok := range progressLog {
		if strings.Contains(tok, "[user]:") || strings.Contains(tok, "[assistant]:") {
			markers++
		}
	}
	if markers > len(history) {
		return len(history)
	}
	return markers
}

func uniqueTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
