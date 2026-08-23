package service

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/oberthci/oberth/pkg/periapsis"
)

type weightedAdmission struct {
	mu       sync.Mutex
	capacity int
	used     int
	waiters  []*admissionReservation
}

type admissionReservation struct {
	gate     *weightedAdmission
	sequence int64
	size     periapsis.Size
	weight   int
	prepared bool
	granted  bool
	delayed  bool
	released bool
	canceled bool
	ready    chan struct{}
}

// newWeightedAdmission builds a gate whose capacity is denominated in size
// weight, not in runs. Callers holding a run-count ceiling must convert with
// newWeightedAdmissionForJobs rather than passing the count directly.
func newWeightedAdmission(capacity int) *weightedAdmission {
	if capacity < 1 {
		capacity = 1
	}
	return &weightedAdmission{capacity: capacity}
}

// newWeightedAdmissionForJobs converts the operator's configured concurrent-run
// ceiling into the weight budget this gate is denominated in.
//
// The two are different units and conflating them silently caps the whole
// server at one run: a budget of maxConcurrentJobs weight admits only
// floor(maxConcurrentJobs / weight) runs, which for the default M size (weight
// 2) is one run at maxConcurrentJobs=3. Scaling by the default size's weight
// makes maxConcurrentJobs runs of an undeclared-size pipeline fit exactly, so
// the configured number is the number an ordinary repository actually gets.
// A pipeline that declares a heavier size then trades concurrency for
// per-run resources proportionally, instead of collapsing the server to one.
func newWeightedAdmissionForJobs(maxConcurrentJobs int) *weightedAdmission {
	if maxConcurrentJobs < 1 {
		maxConcurrentJobs = 1
	}
	return newWeightedAdmission(maxConcurrentJobs * sizeWeight(periapsis.M))
}

// reserve records FIFO claim order before exact-SHA checkout and static
// parsing can finish out of order.
func (gate *weightedAdmission) reserve(sequence int64) *admissionReservation {
	reservation := &admissionReservation{gate: gate, sequence: sequence, ready: make(chan struct{})}
	gate.mu.Lock()
	gate.waiters = append(gate.waiters, reservation)
	sort.SliceStable(gate.waiters, func(i, j int) bool {
		return gate.waiters[i].sequence < gate.waiters[j].sequence
	})
	gate.grantLocked()
	gate.mu.Unlock()
	return reservation
}

func (reservation *admissionReservation) acquire(ctx context.Context, size periapsis.Size) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !size.Valid() {
		reservation.cancel()
		return nil, fmt.Errorf("service: invalid resource size %q", size)
	}
	size = size.Effective()
	gate := reservation.gate
	gate.mu.Lock()
	if reservation.canceled || reservation.released {
		gate.mu.Unlock()
		return nil, context.Canceled
	}
	reservation.size = size
	reservation.weight = min(sizeWeight(size), gate.capacity)
	reservation.prepared = true
	gate.grantLocked()
	granted := reservation.granted
	reservation.delayed = !granted
	gate.mu.Unlock()
	if granted {
		return reservation.release, nil
	}

	select {
	case <-reservation.ready:
		return reservation.release, nil
	case <-ctx.Done():
		gate.mu.Lock()
		if reservation.granted {
			gate.releaseLocked(reservation)
		} else if !reservation.canceled {
			reservation.canceled = true
			gate.removeLocked(reservation)
			gate.grantLocked()
		}
		gate.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (reservation *admissionReservation) wasDelayed() bool {
	gate := reservation.gate
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return reservation.delayed
}

func (reservation *admissionReservation) cancel() {
	gate := reservation.gate
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if reservation.released || reservation.canceled {
		return
	}
	if reservation.granted {
		gate.releaseLocked(reservation)
		return
	}
	reservation.canceled = true
	gate.removeLocked(reservation)
	gate.grantLocked()
}

func (reservation *admissionReservation) release() {
	gate := reservation.gate
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.releaseLocked(reservation)
}

func (gate *weightedAdmission) grantLocked() {
	for len(gate.waiters) > 0 {
		reservation := gate.waiters[0]
		if !reservation.prepared || !gate.canGrantLocked(reservation) {
			return
		}
		gate.waiters = gate.waiters[1:]
		reservation.granted = true
		gate.used += reservation.weight
		close(reservation.ready)
	}
}

// canGrantLocked admits on the weight budget alone, with one exception: XL
// declares the whole node and therefore runs alone.
//
// L carries no separate global exclusion. Its weight already prices it above
// the default size, so the budget throttles it proportionally and an operator
// who raises maxConcurrentJobs actually gets more concurrent large runs. A
// hard "one L at a time" rule instead pinned every L-declaring repository to a
// single run no matter what the operator configured.
func (gate *weightedAdmission) canGrantLocked(reservation *admissionReservation) bool {
	if reservation.size == periapsis.XL {
		return gate.used == 0
	}
	return gate.used+reservation.weight <= gate.capacity
}

func (gate *weightedAdmission) releaseLocked(reservation *admissionReservation) {
	if !reservation.granted || reservation.released {
		return
	}
	reservation.released = true
	gate.used -= reservation.weight
	gate.grantLocked()
}

func (gate *weightedAdmission) removeLocked(reservation *admissionReservation) {
	for index, candidate := range gate.waiters {
		if candidate != reservation {
			continue
		}
		gate.waiters = append(gate.waiters[:index], gate.waiters[index+1:]...)
		return
	}
}

func sizeWeight(size periapsis.Size) int {
	switch size.Effective() {
	case periapsis.S:
		return 1
	case periapsis.M:
		return 2
	case periapsis.L:
		return 3
	case periapsis.XL:
		return int(^uint(0) >> 1)
	default:
		return 0
	}
}
