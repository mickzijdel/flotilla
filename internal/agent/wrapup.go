package agent

import "strings"

// DefaultWrapUp is appended to every agent's prompt so the working tree is clean
// and committed by the time the agent exits (Flotilla submits strictly).
const DefaultWrapUp = "Before you finish, commit all your changes with clear, " +
	"descriptive messages — your commit messages become the pull request. Do not " +
	"leave uncommitted work; anything uncommitted will be discarded and the submission rejected."

// WrapUpText returns the effective wrap-up contract: the profile's override, the
// default when unset, or "" when explicitly disabled with the "-" sentinel.
func (p Profile) WrapUpText() string {
	switch p.WrapUp {
	case "":
		return DefaultWrapUp
	case "-":
		return ""
	default:
		return p.WrapUp
	}
}

// PromptWithWrapUp appends the wrap-up contract to the user prompt as a clearly
// delimited block. An empty contract leaves the prompt unchanged.
func PromptWithWrapUp(prompt, wrapUp string) string {
	if strings.TrimSpace(wrapUp) == "" {
		return prompt
	}
	return prompt + "\n\n---\n[Flotilla submission contract]\n" + wrapUp + "\n"
}
