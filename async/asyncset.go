package async

import (
	"sync"
)

// ConcurrentStringSet is a thread-safe set of string values
type ConcurrentStringSet struct {
	rwlock *sync.RWMutex
	set    map[string]bool
}

// Creates a new ConcurrentStringSet
func NewConcurrentStringSet() *ConcurrentStringSet {
	return &ConcurrentStringSet{
		rwlock: new(sync.RWMutex),
		set:    make(map[string]bool),
	}
}

// Whether or not the set contains a string
func (css *ConcurrentStringSet) Contains(value string) bool {
	css.rwlock.RLock()
	defer css.rwlock.RUnlock()

	return css.set[value]
}

// Adds a string to the set
func (css *ConcurrentStringSet) Add(value string) {
	css.rwlock.Lock()
	defer css.rwlock.Unlock()

	css.set[value] = true
}

// Removes a string from the set
func (css *ConcurrentStringSet) Remove(value string) {
	css.rwlock.Lock()
	defer css.rwlock.Unlock()

	delete(css.set, value)
}
