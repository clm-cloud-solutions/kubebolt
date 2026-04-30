package copilot

import _ "embed"

// Kobi prompt layers, embedded at build time.
//
// Source of truth: edit these files directly. See prompts/README.md.
//
//go:embed prompts/kobi-identity.md
var kobiIdentityPrompt string

//go:embed prompts/kobi-copilot.md
var kobiCopilotPrompt string

//go:embed prompts/kobi-few-shots.md
var kobiFewShotsPrompt string
