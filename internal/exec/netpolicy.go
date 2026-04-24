package exec

// PolicyDecision is the result of checking a skill's network policy.
type PolicyDecision struct {
	Allow  bool
	Reason string
}

// Policy returns the binary network decision for the given skill. v1 is:
//   - explicit deny-list wins (reason: "network policy denies this skill")
//   - otherwise honor the default (allow|deny)
//
// When the default is deny and the skill is not in the (currently
// conceptual) allow-list, the result is Allow=false with the default reason.
func Policy(skill string, cfg *Config) PolicyDecision {
	if _, denied := cfg.NetworkDenySkills[skill]; denied {
		return PolicyDecision{Allow: false, Reason: "network policy denies this skill: " + skill}
	}
	if cfg.NetworkDefault == NetAllow {
		return PolicyDecision{Allow: true}
	}
	return PolicyDecision{Allow: false, Reason: "network policy default denies all skills"}
}
