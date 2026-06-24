package host

import contracts "github.com/Herrscherd/herrscher-contracts"

// supportedRemoteCategories names the plugin categories that can run
// out-of-process today. Hosting (plugin-host) or resolving a category remotely
// is gated on membership here, so adding one is a registration plus its
// dial/skeleton wiring — not a `category == "memory"` branch scattered across
// the host. memory and orchestrator are remote-capable (C1, C2);
// the streaming backend (C3) joins next.
var supportedRemoteCategories = map[contracts.Category]bool{
	contracts.CategoryMemory:       true,
	contracts.CategoryOrchestrator: true,
}

// SupportedRemoteCategory reports whether c can be hosted or resolved remotely.
// An unsupported category stays in-process (the host warns and skips it).
func SupportedRemoteCategory(c contracts.Category) bool {
	return supportedRemoteCategories[c]
}
