## MODIFIED Requirements

### Requirement: Broker uses configurable slot and preempt settings
The LLM broker SHALL read `slot_count` and `preempt_timeout_ms` from the config store at startup, with env var and hardcoded fallbacks. The model name sent to the llama.cpp server SHALL be read from `config:chat` `model` field.

#### Scenario: Slot count from config
- **WHEN** the broker initializes its slot table
- **THEN** it SHALL create the number of slots specified by `slot_count` from `config:broker` (default 4)

#### Scenario: Preempt timeout from config
- **WHEN** the broker executes a preemption
- **THEN** it SHALL wait for `preempt_timeout_ms` from `config:broker` (default 5000) before force-releasing the slot

#### Scenario: Model name from config
- **WHEN** the broker proxies an LLM request to llama.cpp
- **THEN** it SHALL use the `model` value from `config:chat` (default "default") in the request body
