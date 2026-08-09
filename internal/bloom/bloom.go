// Package bloom implements the in-process address filter that decides, without
// touching MySQL, whether a recipient seen on chain can possibly be one of our
// deposit addresses. A negative answer is exact, so the log is dropped
// immediately; a positive answer still has to be resolved against user_wallet.
package bloom

import (
	"math"
	"sync"
)

const (
	fnvOffset1 = uint64(14695981039346656037)
	fnvOffset2 = uint64(1099511628211)
	fnvPrime   = uint64(1099511628211)
)

// Filter is a classic bit-array bloom filter, safe for concurrent use.
type Filter struct {
	mu       sync.RWMutex
	bits     []uint64
	m        uint64 // bit count
	k        uint64 // hash count
	n        uint64 // inserted keys
	capacity uint64 // key count the filter was sized for
}

// New sizes a filter for expected keys at the target false positive rate.
func New(expected uint64, fpRate float64) *Filter {
	if expected == 0 {
		expected = 1
	}
	if fpRate <= 0 || fpRate >= 1 {
		fpRate = 0.0001
	}
	m, k := Estimate(expected, fpRate)
	return &Filter{
		bits:     make([]uint64, (m+63)/64),
		m:        m,
		k:        k,
		capacity: expected,
	}
}

// Estimate returns the optimal bit count and hash count for the given load.
func Estimate(expected uint64, fpRate float64) (m, k uint64) {
	bits := -float64(expected) * math.Log(fpRate) / (math.Ln2 * math.Ln2)
	m = uint64(math.Ceil(bits))
	if m < 64 {
		m = 64
	}
	k = uint64(math.Round(float64(m) / float64(expected) * math.Ln2))
	if k < 1 {
		k = 1
	}
	if k > 32 {
		k = 32
	}
	return m, k
}

func (f *Filter) Add(key string) {
	if f == nil || key == "" {
		return
	}
	h1, h2 := hashes(key)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := uint64(0); i < f.k; i++ {
		idx := (h1 + i*h2) % f.m
		f.bits[idx/64] |= 1 << (idx % 64)
	}
	f.n++
}

// MayContain reports whether key may have been added. False is definitive.
// A nil filter answers true so callers degrade to the database lookup.
func (f *Filter) MayContain(key string) bool {
	if f == nil {
		return true
	}
	if key == "" {
		return false
	}
	h1, h2 := hashes(key)
	f.mu.RLock()
	defer f.mu.RUnlock()
	for i := uint64(0); i < f.k; i++ {
		idx := (h1 + i*h2) % f.m
		if f.bits[idx/64]&(1<<(idx%64)) == 0 {
			return false
		}
	}
	return true
}

// Count is the number of Add calls, duplicates included.
func (f *Filter) Count() uint64 {
	if f == nil {
		return 0
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.n
}

// Capacity is the key count the filter was sized for. Beyond it the false
// positive rate degrades and the owner should rebuild a larger filter.
func (f *Filter) Capacity() uint64 {
	if f == nil {
		return 0
	}
	return f.capacity
}

func (f *Filter) Bits() uint64 { return f.m }

func (f *Filter) Hashes() uint64 { return f.k }

// FalsePositiveRate estimates the current rate for the keys inserted so far.
func (f *Filter) FalsePositiveRate() float64 {
	if f == nil {
		return 1
	}
	f.mu.RLock()
	n := float64(f.n)
	f.mu.RUnlock()
	exp := -float64(f.k) * n / float64(f.m)
	return math.Pow(1-math.Exp(exp), float64(f.k))
}

// hashes derives the two 64 bit base hashes of the Kirsch-Mitzenmacher scheme,
// which yields k indexes from two FNV-1a variants without any allocation.
func hashes(key string) (uint64, uint64) {
	h1 := fnvOffset1
	h2 := fnvOffset2
	for i := 0; i < len(key); i++ {
		h1 = (h1 ^ uint64(key[i])) * fnvPrime
		h2 = (h2 + uint64(key[i])) * fnvPrime
		h2 ^= h2 >> 29
	}
	if h2%2 == 0 {
		h2++ // an even step can only reach half of the bit positions
	}
	return h1, h2
}
