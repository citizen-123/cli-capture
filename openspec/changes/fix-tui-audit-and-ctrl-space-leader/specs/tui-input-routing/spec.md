## Purpose

Provide a consistent global leader and accurate controls across the hosted terminal, traffic pane, and modal views without losing drafts or forwarding application shortcuts.

## ADDED Requirements

### Requirement: Ctrl+Space is the default leader
The application SHALL use Ctrl+Space when no explicit leader is configured. It SHALL recognize the NUL runtime identity shared by Ctrl+Space and Ctrl+@. Explicit CLI configuration SHALL override file configuration, and file configuration SHALL override the default. Ctrl+A SHALL NOT remain an implicit alternate leader.

#### Scenario: Default terminal encoding
- **WHEN** a terminal sends NUL followed by the default pane-switch command with no configured leader
- **THEN** the application switches panes without forwarding either command byte to the child
- **AND** an ordinary Ctrl+A reaches the child while the terminal owns input

#### Scenario: Explicit legacy leader
- **WHEN** an operator explicitly configures Ctrl+A as the leader
- **THEN** Ctrl+A remains the effective leader and the interface displays that override

#### Scenario: Literal leader passthrough
- **WHEN** the terminal owns input and the operator presses the effective leader twice
- **THEN** the child receives exactly one literal leader byte
- **AND** no pane action occurs

### Requirement: Leader commands precede modal text input
The application SHALL dispatch configured leader commands before terminal, detail, editor, filter, command-line, help, and Repeater input. Command bytes SHALL NOT enter a draft or the child terminal. Unknown leader commands SHALL leave content unchanged and display actionable feedback. A pending leader SHALL be visible and cancellable with Escape.

#### Scenario: Global actions inside a draft
- **WHEN** an operator invokes global save, export, intercept toggle, resize, or mouse-capture toggle from an editor
- **THEN** the requested action occurs once and the draft and cursor remain unchanged

#### Scenario: Help preserves its origin
- **WHEN** an operator opens global help from a draft and closes help
- **THEN** the application returns to the same draft, input owner, and cursor position

#### Scenario: Global pane switch suspends a modal
- **WHEN** an operator switches to the terminal from a modal and later switches back
- **THEN** the terminal receives ordinary input during the suspension
- **AND** the original modal resumes without draft loss or an implicit send

#### Scenario: Quit from any view
- **WHEN** an operator invokes the configured global quit command from any view
- **THEN** the application exits through its normal shutdown path without inserting command text

### Requirement: Leader collisions fail clearly
The application SHALL reject bindings that conflict with the effective leader's runtime identity in any context. Ctrl+Space and Ctrl+@ SHALL be equivalent for collision detection. An Alt-modified control key SHALL remain distinct from an unmodified leader.

#### Scenario: Modal NUL collision
- **WHEN** configuration binds Ctrl+@ to a modal action while the leader is Ctrl+Space
- **THEN** startup fails with the conflicting context, key, and effective leader identified
- **AND** the diagnostic explains that the operator must choose another binding

### Requirement: Action hints use the effective keymap
Startup hints, status messages, modal footers, CLI help, and the help overlay SHALL describe the actual available bindings. Unbound actions SHALL NOT display nonexistent shortcuts. The displayed default leader name SHALL be Ctrl+Space rather than its terminal alias.

#### Scenario: Customized controls
- **WHEN** the operator configures Ctrl+G and rebinds editor send
- **THEN** startup hints, help, and editor instructions display the effective controls rather than built-in defaults

#### Scenario: Terminal reserves the default chord
- **WHEN** the operator consults leader configuration instructions
- **THEN** the instructions explain NUL encoding and provide an explicit alternate-leader command for terminals or multiplexers that reserve Ctrl+Space
