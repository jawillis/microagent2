## ADDED Requirements

### Requirement: Manual retro job trigger endpoint
The system SHALL provide an API endpoint `POST /v1/retro/:session/trigger` that accepts a JSON body with a `job_type` field. Valid job types are `memory_extraction`, `skill_creation`, and `curation`.

#### Scenario: Trigger memory extraction
- **WHEN** a POST request is made to `/v1/retro/:session/trigger` with `{"job_type": "memory_extraction"}`
- **THEN** the system SHALL publish a retro trigger message to the retro agent's request stream for the specified session

#### Scenario: Invalid job type
- **WHEN** a POST request is made with an unrecognized `job_type`
- **THEN** the system SHALL return HTTP 400 with an error message listing valid job types

#### Scenario: Session does not exist
- **WHEN** a POST request is made for a session ID that has no history in Valkey
- **THEN** the system SHALL return HTTP 404

### Requirement: Retro job locking
The system SHALL acquire a Valkey lock key `retro:lock:<session>:<job_type>` with SET NX and a TTL of 300 seconds before starting a retro job. If the lock already exists, the job SHALL be rejected.

#### Scenario: Lock acquired successfully
- **WHEN** a retro job is triggered and no lock exists for that session and job type
- **THEN** the system SHALL set the lock key, start the job, and return HTTP 202 with a confirmation message

#### Scenario: Lock already held
- **WHEN** a retro job is triggered and a lock already exists for that session and job type
- **THEN** the system SHALL return HTTP 409 with a message indicating the job is already running

### Requirement: Lock cleanup on job completion
The retro agent SHALL delete the lock key `retro:lock:<session>:<job_type>` when a job completes (successfully or with error). If the lock has already expired (TTL elapsed), no error SHALL be raised.

#### Scenario: Job completes and lock is cleared
- **WHEN** a retro job finishes execution
- **THEN** the lock key SHALL be deleted from Valkey

#### Scenario: Lock expired before job completes
- **WHEN** a retro job finishes but the lock TTL has already expired
- **THEN** the cleanup SHALL not raise an error
