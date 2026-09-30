package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/model"
)

// killRecordingService is an allocation service with fixed registered allocations that records
// the signals it is sent.
type killRecordingService struct {
	AllocationService

	mu         sync.Mutex
	registered []model.AllocationID
	listed     chan struct{}
	signals    chan AllocationSignal
}

func (s *killRecordingService) GetAllAllocationIDs() []model.AllocationID {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.listed <- struct{}{}:
	default:
	}
	return s.registered
}

func (s *killRecordingService) Signal(_ model.AllocationID, sig AllocationSignal, _ string) error {
	s.signals <- sig
	return nil
}

// failFirstRead makes the first read of whether a job was asked to stop fail, and later reads
// return requested. It returns the number of reads so far.
func failFirstRead(t *testing.T, requested bool) func() int {
	var mu sync.Mutex
	reads := 0
	old := readCancelRequested
	t.Cleanup(func() { readCancelRequested = old })
	readCancelRequested = func(context.Context, model.JobID) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads == 1 {
			return false, errors.New("connection reset")
		}
		return requested, nil
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return reads
	}
}

func useService(t *testing.T, registered ...model.AllocationID) *killRecordingService {
	service := &killRecordingService{
		registered: registered,
		listed:     make(chan struct{}, 1),
		signals:    make(chan AllocationSignal, 1),
	}
	old := DefaultService
	t.Cleanup(func() { DefaultService = old })
	DefaultService = service
	return service
}

func TestKillIfCancelRequestedRetriesAFailedRead(t *testing.T) {
	const jobID, allocationID = model.JobID("job"), model.AllocationID("task.1")

	// A cancel that committed before the allocation was registered found nothing to signal, so a
	// failed read must not lose the kill.
	service := useService(t, allocationID)
	reads := failFirstRead(t, true)
	KillIfCancelRequested(context.Background(), jobID, allocationID)
	select {
	case sig := <-service.signals:
		require.Equal(t, KillAllocation, sig)
	case <-time.After(10 * time.Second):
		t.Fatal("the allocation of a job asked to stop was never killed")
	}
	require.Equal(t, 2, reads())

	// A job that was not asked to stop is left alone once a retry reads it.
	service = useService(t, allocationID)
	reads = failFirstRead(t, false)
	KillIfCancelRequested(context.Background(), jobID, allocationID)
	require.Eventually(t, func() bool { return reads() == 2 }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, service.signals)

	// The retry stops once the allocation is gone.
	service = useService(t)
	reads = failFirstRead(t, true)
	KillIfCancelRequested(context.Background(), jobID, allocationID)
	select {
	case <-service.listed:
	case <-time.After(10 * time.Second):
		t.Fatal("the retry never checked whether the allocation is registered")
	}
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, reads())
	require.Empty(t, service.signals)
}

func TestKillIfCancelRequestedReadsOnce(t *testing.T) {
	const jobID, allocationID = model.JobID("job"), model.AllocationID("task.1")
	for _, requested := range []bool{false, true} {
		service := useService(t, allocationID)
		reads := 0
		old := readCancelRequested
		t.Cleanup(func() { readCancelRequested = old })
		readCancelRequested = func(context.Context, model.JobID) (bool, error) {
			reads++
			return requested, nil
		}
		KillIfCancelRequested(context.Background(), jobID, allocationID)
		require.Equal(t, 1, reads, "the first read is synchronous")
		if requested {
			select {
			case sig := <-service.signals:
				require.Equal(t, KillAllocation, sig)
			case <-time.After(10 * time.Second):
				t.Fatal("the allocation of a job asked to stop was never killed")
			}
		} else {
			time.Sleep(50 * time.Millisecond)
			require.Empty(t, service.signals)
		}
	}
}
