package discord

import "strings"

const choiceCustomIDPrefix = "dctlchoice:"

// ChoiceCustomID builds the custom_id carried by a session's choice select menu.
func ChoiceCustomID(session string) string { return choiceCustomIDPrefix + session }

// ParseChoiceCustomID extracts the session name from a choice-menu custom_id and
// reports whether the id is a choice menu at all.
func ParseChoiceCustomID(id string) (string, bool) {
	return strings.CutPrefix(id, choiceCustomIDPrefix)
}
