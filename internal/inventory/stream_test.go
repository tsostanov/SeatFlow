package inventory

import (
	"context"
	"slices"
	"testing"

	pb "github.com/tsostanov/SeatFlow/gen/booking/v1"
)

func TestAvailabilityHubFansOutLatestSnapshot(t *testing.T) {
	hubCtx, cancel := context.WithCancel(context.Background())
	hub := &availabilityHub{
		current:     &pb.AvailabilityResponse{SeatIds: []int64{1, 2}, AvailableSeatIds: []int64{1, 2}},
		subscribers: make(map[chan availabilityUpdate]struct{}), ctx: hubCtx, cancel: cancel,
	}
	service := &Service{watchers: map[int64]*availabilityHub{1: hub}}
	first, unsubscribeFirst := service.subscribeAvailability(1, nil)
	second, unsubscribeSecond := service.subscribeAvailability(1, nil)
	if (<-first).value == nil || (<-second).value == nil {
		t.Fatal("subscribers did not receive the initial snapshot")
	}

	service.publishAvailability(1, hub, &pb.AvailabilityResponse{SeatIds: []int64{1, 2}, AvailableSeatIds: []int64{2}})
	service.publishAvailability(1, hub, &pb.AvailabilityResponse{SeatIds: []int64{1, 2}, AvailableSeatIds: []int64{1}})
	for name, updates := range map[string]<-chan availabilityUpdate{"first": first, "second": second} {
		update := <-updates
		if update.err != nil || !slices.Equal(update.value.AvailableSeatIds, []int64{1}) {
			t.Fatalf("%s received %+v", name, update)
		}
	}

	unsubscribeFirst()
	unsubscribeFirst()
	unsubscribeSecond()
	service.watchMu.Lock()
	defer service.watchMu.Unlock()
	if len(service.watchers) != 0 || hubCtx.Err() == nil {
		t.Fatalf("hub was not stopped: watchers=%d context=%v", len(service.watchers), hubCtx.Err())
	}
}
