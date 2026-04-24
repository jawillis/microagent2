package exec

import "testing"

func TestPolicy_DefaultAllowLetsThrough(t *testing.T) {
	cfg := &Config{NetworkDefault: NetAllow, NetworkDenySkills: map[string]struct{}{}}
	d := Policy("anything", cfg)
	if !d.Allow {
		t.Fatalf("allow expected, got %+v", d)
	}
}

func TestPolicy_DenyListWins(t *testing.T) {
	cfg := &Config{NetworkDefault: NetAllow, NetworkDenySkills: map[string]struct{}{"suspect": {}}}
	d := Policy("suspect", cfg)
	if d.Allow {
		t.Fatalf("suspect should be denied: %+v", d)
	}
	if d.Reason == "" {
		t.Fatalf("reason should be populated")
	}
}

func TestPolicy_DefaultDenyDeniesAll(t *testing.T) {
	cfg := &Config{NetworkDefault: NetDeny, NetworkDenySkills: map[string]struct{}{}}
	d := Policy("any", cfg)
	if d.Allow {
		t.Fatalf("default deny should deny: %+v", d)
	}
}

func TestPolicy_OtherSkillsAllowedWhenOneDenied(t *testing.T) {
	cfg := &Config{NetworkDefault: NetAllow, NetworkDenySkills: map[string]struct{}{"bad": {}}}
	if d := Policy("good", cfg); !d.Allow {
		t.Fatalf("good should be allowed: %+v", d)
	}
}
