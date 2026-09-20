package rm

import (
	"testing"

	"github.com/determined-ai/determined/master/internal/config"
)

type schedulerConfigRM struct {
	ResourceManager
	scheduler *config.SchedulerConfig
	exists    bool
}

func (m schedulerConfigRM) ResourcePoolSchedulerConfig(string) (*config.SchedulerConfig, bool) {
	return m.scheduler, m.exists
}

func TestLivePoolSchedulerDefaults(t *testing.T) {
	priority := 17
	for _, tc := range []struct {
		name       string
		scheduler  *config.SchedulerConfig
		priority   int
		preemption bool
	}{
		{"priority", &config.SchedulerConfig{Priority: &config.PrioritySchedulerConfig{
			DefaultPriority: &priority, Preemption: true,
		}}, priority, true},
		{"priority-default", &config.SchedulerConfig{Priority: &config.PrioritySchedulerConfig{}}, 42, false},
		{"fair-share", &config.SchedulerConfig{FairShare: &config.FairShareSchedulerConfig{}}, 42, true},
		{"round-robin", &config.SchedulerConfig{RoundRobin: &config.RoundRobinSchedulerConfig{}}, 42, false},
		{"no-scheduler", nil, 42, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := schedulerConfigRM{scheduler: tc.scheduler, exists: true}
			if got := DefaultPriorityForPool(manager, "dynamic"); got != tc.priority {
				t.Fatalf("priority = %d; want %d", got, tc.priority)
			}
			if got := ReadRMPreemptionStatus(manager, "dynamic"); got != tc.preemption {
				t.Fatalf("preemption = %v; want %v", got, tc.preemption)
			}
		})
	}
}

func TestMissingLivePoolUsesStaticPriorityFallback(t *testing.T) {
	for _, manager := range []ResourceManager{nil, schedulerConfigRM{exists: false}} {
		if got := DefaultPriorityForPool(manager, "not-configured"); got != config.DefaultSchedulingPriority {
			t.Fatalf("missing pool priority = %d", got)
		}
	}
}

func TestLivePoolPreservesStaticManagerPriorityFallback(t *testing.T) {
	masterConfig := config.GetMasterConfig()
	original := masterConfig.ResourceConfig
	t.Cleanup(func() { masterConfig.ResourceConfig = original })
	priority := 19
	masterConfig.ResourceConfig = config.ResourceConfig{
		RootManagerInternal: &config.ResourceManagerConfig{
			AgentRM: &config.AgentResourceManagerConfig{
				Scheduler: &config.SchedulerConfig{
					Priority: &config.PrioritySchedulerConfig{DefaultPriority: &priority},
				},
			},
		},
		RootPoolsInternal: []config.ResourcePoolConfig{{PoolName: "static"}},
	}
	manager := schedulerConfigRM{
		scheduler: &config.SchedulerConfig{FairShare: &config.FairShareSchedulerConfig{}},
		exists:    true,
	}
	if got := DefaultPriorityForPool(manager, "static"); got != priority {
		t.Fatalf("static manager priority fallback = %d; want %d", got, priority)
	}
}
