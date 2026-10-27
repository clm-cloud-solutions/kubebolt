package copilot

import (
	"strings"
	"testing"
)

// A chat wording for a tool that no longer exists, or that the catalogue
// stopped serving read-only, would be dead text nobody notices.
func TestChatDescriptions_NameReadOnlyCatalogueTools(t *testing.T) {
	readOnly := map[string]string{}
	for _, d := range GovernedToolDefinitions(false, false) {
		readOnly[d.Name] = d.Description
	}
	for name, chat := range chatDescriptions {
		full, ok := readOnly[name]
		if !ok {
			t.Errorf("%s: not a read-only catalogue tool", name)
			continue
		}
		if len(chat) >= len(full) {
			t.Errorf("%s: the chat wording (%d bytes) is not shorter than the catalogue's (%d)", name, len(chat), len(full))
		}
	}
	for _, d := range ChatToolDefinitions(true, true) {
		if want := chatDescriptions[d.Name]; want != "" && d.Description != want {
			t.Errorf("%s: the chat does not use its own wording", d.Name)
		}
	}
	for _, d := range ToolDefinitions() {
		if chat := chatDescriptions[d.Name]; chat != "" && strings.EqualFold(chat, d.Description) {
			t.Errorf("%s: the catalogue serves the chat wording", d.Name)
		}
	}
}

// An argument wording for an argument the tool no longer has is dead text.
func TestChatProperties_NameExistingArguments(t *testing.T) {
	chat := map[string]ToolDefinition{}
	for _, d := range ChatToolDefinitions(true, true) {
		chat[d.Name] = d
	}
	for tool, args := range chatProperties {
		def, ok := chat[tool]
		if !ok {
			t.Errorf("%s: not a chat tool", tool)
			continue
		}
		props, _ := def.InputSchema["properties"].(map[string]interface{})
		for arg, want := range args {
			p, ok := props[arg].(map[string]interface{})
			if !ok {
				t.Errorf("%s.%s: no such argument", tool, arg)
				continue
			}
			if p["description"] != want {
				t.Errorf("%s.%s: the chat does not use its wording", tool, arg)
			}
		}
	}
	for _, d := range ToolDefinitions() {
		props, _ := d.InputSchema["properties"].(map[string]interface{})
		for arg, want := range chatProperties[d.Name] {
			if p, ok := props[arg].(map[string]interface{}); ok && p["description"] == want {
				t.Errorf("%s.%s: the catalogue serves the chat wording", d.Name, arg)
			}
		}
	}
}
