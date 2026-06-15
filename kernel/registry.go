package kernel

// Registry is held by the host. Plugins register into it; the host queries by
// category. In Phase 1 the in-process registration becomes NATS
// self-registration with the same Manifest and the same query surface.
type Registry struct {
	gateways []Gateway
}

func (r *Registry) RegisterGateway(g Gateway) { r.gateways = append(r.gateways, g) }
func (r *Registry) Gateways() []Gateway       { return r.gateways }
