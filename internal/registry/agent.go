package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jasonwillis/microagent2/internal/messaging"
)

type AgentInfo struct {
	AgentID            string
	Priority           int
	Preemptible        bool
	Capabilities       []string
	Trigger            string
	HeartbeatIntervalMS int
	LastHeartbeat      time.Time
	Alive              bool
}

type AgentRegistrar struct {
	client  *messaging.Client
	agentID string
	info    messaging.RegisterPayload
}

func NewAgentRegistrar(client *messaging.Client, info messaging.RegisterPayload) *AgentRegistrar {
	return &AgentRegistrar{
		client:  client,
		agentID: info.AgentID,
		info:    info,
	}
}

func (r *AgentRegistrar) Register(ctx context.Context) error {
	msg, err := messaging.NewMessage(messaging.TypeRegister, r.agentID, r.info)
	if err != nil {
		return err
	}
	_, err = r.client.Publish(ctx, messaging.StreamRegistryAnnounce, msg)
	return err
}

func (r *AgentRegistrar) Deregister(ctx context.Context) error {
	payload := messaging.DeregisterPayload{AgentID: r.agentID}
	msg, err := messaging.NewMessage(messaging.TypeDeregister, r.agentID, payload)
	if err != nil {
		return err
	}
	_, err = r.client.Publish(ctx, messaging.StreamRegistryAnnounce, msg)
	return err
}

func (r *AgentRegistrar) RunHeartbeat(ctx context.Context) {
	interval := time.Duration(r.info.HeartbeatIntervalMS) * time.Millisecond
	channel := fmt.Sprintf(messaging.ChannelHeartbeat, r.agentID)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg, err := messaging.NewMessage(messaging.TypeHeartbeat, r.agentID, nil)
			if err != nil {
				continue
			}
			_ = r.client.PubSubPublish(ctx, channel, msg)
		}
	}
}

type Registry struct {
	mu     sync.RWMutex
	agents map[string]*AgentInfo
}

func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[string]*AgentInfo),
	}
}

func (r *Registry) Register(info *AgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info.Alive = true
	info.LastHeartbeat = time.Now()
	r.agents[info.AgentID] = info
}

func (r *Registry) Deregister(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, agentID)
}

func (r *Registry) Get(agentID string) (*AgentInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.agents[agentID]
	return info, ok
}

func (r *Registry) MarkHeartbeat(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.agents[agentID]; ok {
		info.LastHeartbeat = time.Now()
		info.Alive = true
	}
}

func (r *Registry) MarkDead(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.agents[agentID]; ok {
		info.Alive = false
	}
}

func (r *Registry) ListAlive() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AgentInfo
	for _, info := range r.agents {
		if info.Alive {
			result = append(result, info)
		}
	}
	return result
}

func (r *Registry) ListPreemptible() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AgentInfo
	for _, info := range r.agents {
		if info.Alive && info.Preemptible {
			result = append(result, info)
		}
	}
	return result
}

func (r *Registry) ListDead() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AgentInfo
	for _, info := range r.agents {
		if !info.Alive {
			result = append(result, info)
		}
	}
	return result
}
