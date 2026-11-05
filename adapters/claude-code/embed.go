// Package skilldoc holds the Claude skill embedded in the agentnet binary.
package skilldoc

import _ "embed"

//go:embed SKILL.md
var Markdown []byte
