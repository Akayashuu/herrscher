package contracts

// Conversation is an opaque address into a chat platform. Comparable so it can
// key a map (Conversation -> SessionID).
type Conversation struct {
	Gateway string
	ID      string
}

type (
	SessionID string
	MessageID string
)

type Choice struct {
	Label string
	Value string
}

type Attachment struct {
	Filename string
	URL      string
}
