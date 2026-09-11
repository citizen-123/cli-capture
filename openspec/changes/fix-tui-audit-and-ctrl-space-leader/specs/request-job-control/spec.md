## Purpose

Keep the TUI responsive during operator-triggered requests and attacks, with accurate previews, visible progress, cancellable execution, and correctly associated results.

## ADDED Requirements

### Requirement: Network jobs do not block interaction
Resend and Repeater network work SHALL NOT block keyboard input, mouse input, rendering, or live capture. The application SHALL display the active job state. A second manual job SHALL NOT start while another manual job remains active. Ordinary captured traffic SHALL continue independently.

#### Scenario: Slow resend
- **WHEN** a resend targets a server that holds its response for three seconds
- **THEN** the operator can open help, navigate, and switch panes before the response completes
- **AND** the interface displays that resend is active

#### Scenario: Repeated send command
- **WHEN** the operator requests another resend or attack while a manual job is active
- **THEN** no duplicate manual job starts
- **AND** the interface identifies the active job and its cancellation control

### Requirement: Attack previews match execution
Repeater SHALL display the planned request count before sending. Counts SHALL follow the existing payload-selection and mode semantics without generating the complete combination set. Invalid templates or unrepresentable counts SHALL display an error and SHALL NOT start a job.

#### Scenario: Two payload dimensions
- **WHEN** two referenced variables have two and three payload values
- **THEN** the previews show single 1, sniper 5, battering-ram 2, pitchfork 2, and cluster-bomb 6
- **AND** the actual request combinations match the selected mode

#### Scenario: No insertion points
- **WHEN** a valid template has no payload-backed insertion points
- **THEN** previews retain existing semantics: single and cluster-bomb show one, and sniper, battering-ram, and pitchfork show zero
- **AND** a zero-count job sends no requests

#### Scenario: Count exceeds the supported numeric range
- **WHEN** the selected payload product cannot be represented safely
- **THEN** the preview reports that the count is too large and disables send without allocating the combination set

### Requirement: Progress reports actual outcomes
The interface SHALL distinguish planned, attempted, succeeded, failed, and canceled work. Each produced exchange SHALL enter the traffic list with its payload label. A failed exchange SHALL NOT count as a successful request. Repeater SHALL display the current job's response or error, not an unmarked response from an earlier job.

#### Scenario: Mixed attack results
- **WHEN** an attack completes with both successful responses and network failures
- **THEN** the interface reports both outcome counts and retains inspectable exchange results
- **AND** it does not report all attempts as successful

#### Scenario: Starting after a previous single response
- **WHEN** the operator starts an attack after a completed single send
- **THEN** the response area clears or explicitly identifies the old result until a current result arrives
- **AND** the title identifies the edited target rather than only the original captured URL

### Requirement: Jobs support explicit cancellation
The application SHALL expose a global cancel-job action, bound by default to leader then `c`, and a `:cancel` command. Cancellation SHALL stop future attack requests and cancel the active network request. Completed results SHALL remain inspectable. Closing Repeater with Escape SHALL leave its job active and visibly cancellable. Application shutdown SHALL cancel any active job.

#### Scenario: Cancel a held request
- **WHEN** the operator cancels a job while its response is held
- **THEN** the client stops waiting without waiting for the ordinary network timeout
- **AND** the interface reports cancellation rather than success

#### Scenario: Cancel an attack between requests
- **WHEN** cancellation occurs after some attack requests finish
- **THEN** no later requests start
- **AND** completed results and accurate progress remain available

### Requirement: Results belong to their originating job
A completed or delayed result SHALL update only its originating job and Repeater session. Opening another flow SHALL NOT relabel or replace that session with an older job's response.

#### Scenario: Close and reopen during an active job
- **WHEN** the operator closes Repeater, opens a different flow in Repeater, and the original request completes
- **THEN** the original exchange remains in the traffic list
- **AND** the new Repeater does not display it as its own response
