package async

import (
	"sync"
	"sync/atomic"
)

// Thread safe atomic bool
type AtomicBool int32

// NewAtomicBool creates an AtomicBool with given default value
func NewAtomicBool(value bool) *AtomicBool {
	ab := new(AtomicBool)
	ab.Set(value)
	return ab
}

// Loads the bool value atomically
func (ab *AtomicBool) Get() bool {
	return atomic.LoadInt32((*int32)(ab)) != 0	
}

// Sets the bool value atomically
func (ab *AtomicBool) Set(value bool) {
	if value {
		atomic.StoreInt32((*int32)(ab), 1)
	} else {
		atomic.StoreInt32((*int32)(ab), 0)
	}
}

// CompareAndSet sets value to new if current is equal to the current value
func (ab *AtomicBool) CompareAndSet(current, new bool) bool {
	var o, n int32
	if current {
		o = 1
	}
	if new {
		n = 1
	}
	return atomic.CompareAndSwapInt32((*int32)(ab), o, n)
}

// WaitChannel behaves like a WaitGroup, but returns a channel on Wait()
type WaitChannel interface {
	Add(delta int)
	Done()
	Wait() <-chan struct{}
}

type waitChannelImpl struct {
	waiting     *AtomicBool
	waitGroup   *sync.WaitGroup
	waitChannel chan struct{}
}

func NewWaitChannel() WaitChannel {
	return &waitChannelImpl{
		waiting:     NewAtomicBool(false),
		waitGroup:   new(sync.WaitGroup),
		waitChannel: nil,
	}
}

func (wc *waitChannelImpl) Add(delta int) {
	wc.waitGroup.Add(delta)
}

func (wc *waitChannelImpl) Done() {
	wc.waitGroup.Done()
}

func (wc *waitChannelImpl) Wait() <-chan struct{} {
	if !wc.waiting.CompareAndSet(false, true) {
		return wc.waitChannel
	}

	wc.waitChannel = make(chan struct{})

	go func() {
		defer func(waitChannel chan struct{}) {
			defer close(waitChannel)
			wc.waiting.CompareAndSet(true, false)
			wc.waitChannel = nil
		}(wc.waitChannel)

		wc.waitGroup.Wait()
	}()

	return wc.waitChannel
}
