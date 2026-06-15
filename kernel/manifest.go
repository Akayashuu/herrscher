package kernel

type Category string

const (
	CategoryGateway      Category = "gateway"
	CategoryBackend      Category = "backend"
	CategoryMemory       Category = "memory"
	CategoryOrchestrator Category = "orchestrator"
)

// Capabilities are announced by a plugin. The degrading decorator reads them to
// rabat unsupported actions. This is the single source of truth (no separate
// Capabilities() method on the port).
type Capabilities struct {
	Reactions   bool
	SelectMenus bool
	Replies     bool
}

// Manifest is what a plugin announces about itself. In Phase 1 this becomes the
// payload of the NATS self-registration; the shape stays identical.
type Manifest struct {
	Kind         string
	Category     Category
	Capabilities Capabilities
}
