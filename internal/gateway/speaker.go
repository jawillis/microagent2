package gateway

import (
	"context"
	"net/http"

	"microagent2/internal/config"
	"microagent2/internal/response"
)

// resolveSpeakerID resolves the speaker for a turn using the 5-step
// precedence: body → header → previous-turn → primary_user_id → "unknown".
func resolveSpeakerID(
	ctx context.Context,
	bodySpeakerID string,
	r *http.Request,
	prevResponseID string,
	responses *response.Store,
	cfgStore *config.Store,
) string {
	if bodySpeakerID != "" {
		return bodySpeakerID
	}

	if h := r.Header.Get("X-Speaker-ID"); h != "" {
		return h
	}

	if prevResponseID != "" && responses != nil {
		if inherited, err := responses.InheritSpeakerID(ctx, prevResponseID); err == nil && inherited != "" {
			return inherited
		}
	}

	var memCfg config.MemoryConfig
	_ = cfgStore.Load(ctx, config.KeyMemory, &memCfg)
	if memCfg.PrimaryUserID != "" {
		return memCfg.PrimaryUserID
	}

	return "unknown"
}
