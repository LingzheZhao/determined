package multirm

import (
	"testing"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/rm"
)

type schedulerProvider struct {
	rm.ResourceManager
	poolName  string
	scheduler *config.SchedulerConfig
}

func (p schedulerProvider) ResourcePoolSchedulerConfig(name string) (*config.SchedulerConfig, bool) {
	return p.scheduler, name == p.poolName
}

func TestResourcePoolSchedulerConfigRouting(t *testing.T) {
	scheduler := config.DefaultSchedulerConfig()
	// The embedded ResourceManagers are nil: looking up configuration must never call the
	// statistics/admission routing path, which can hold scheduler and job locks.
	router := New("first", map[string]rm.ResourceManager{
		"first":  schedulerProvider{poolName: "static", scheduler: nil},
		"second": schedulerProvider{poolName: "dynamic", scheduler: scheduler},
		"other":  nil,
	})
	if got, exists := router.ResourcePoolSchedulerConfig("dynamic"); !exists || got != scheduler {
		t.Fatalf("dynamic pool configuration not routed: got %v, exists %v", got, exists)
	}
	if got, exists := router.ResourcePoolSchedulerConfig("static"); !exists || got != nil {
		t.Fatalf("nil effective scheduler must still report an existing pool")
	}
	if _, exists := router.ResourcePoolSchedulerConfig("missing"); exists {
		t.Fatal("missing pool reported as Ready")
	}
}
