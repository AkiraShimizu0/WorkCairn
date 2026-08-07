package kernel

// LifecycleState is the closed set of Kernel lifecycle states.
type LifecycleState string

const (
	StateStarted LifecycleState = "started"
	StateStopped LifecycleState = "stopped"
)

// KernelStatus is a serializable snapshot suitable for CLI/API adapters.
type KernelStatus struct {
	State              LifecycleState `json:"state"`
	Version            string         `json:"version"`
	RegisteredServices []ServiceKind  `json:"registered_services"`
}
