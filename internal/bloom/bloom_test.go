package bloom

import (
	"fmt"
	"sync"
	"testing"
)

func TestFilterNeverMissesAnAddedAddress(t *testing.T) {
	f := NewBloomFilter(20000, 0.0001)
	addrs := make([]string, 20000)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("TR7NHqjeKQxGTCi8q8ZY4pL8otSzgj%05d", i)
		f.Add(addrs[i])
	}
	// A false negative would silently drop a user deposit, so it must be
	// impossible by construction.
	for _, a := range addrs {
		if !f.MayContain(a) {
			t.Fatalf("address %s was added but is reported as unknown", a)
		}
	}
	if f.Count() != uint64(len(addrs)) {
		t.Fatalf("count = %d, want %d", f.Count(), len(addrs))
	}
}

func TestFilterRejectsUnknownAddressesAtTheConfiguredRate(t *testing.T) {
	const n = 20000
	f := NewBloomFilter(n, 0.0001)
	for i := 0; i < n; i++ {
		f.Add(fmt.Sprintf("known-%05d", i))
	}
	falsePositives := 0
	const probes = 200000
	for i := 0; i < probes; i++ {
		if f.MayContain(fmt.Sprintf("unknown-%06d", i)) {
			falsePositives++
		}
	}
	// 0.0001 target; allow a wide margin so the test does not depend on the
	// exact hash distribution.
	if rate := float64(falsePositives) / probes; rate > 0.001 {
		t.Fatalf("false positive rate = %f, want <= 0.001 (%d of %d)", rate, falsePositives, probes)
	}
}

func TestFilterEmptyKeyIsNeverAMatch(t *testing.T) {
	f := NewBloomFilter(10, 0.01)
	if f.MayContain("") {
		t.Fatal("an empty address must not match")
	}
}

func TestNilFilterFallsBackToTheDatabase(t *testing.T) {
	var f *BloomFilter
	f.Add("TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t")
	if !f.MayContain("TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t") {
		t.Fatal("an uninitialised filter must answer true so the caller queries mysql")
	}
}

func TestEstimateSizesTheFilter(t *testing.T) {
	m, k := Estimate(20000, 0.0001)
	// 20k keys at 1e-4 need roughly 19 bits and 13 hashes per key.
	if m < 350_000 || m > 420_000 {
		t.Fatalf("bits = %d, want ~383000", m)
	}
	if k < 10 || k > 16 {
		t.Fatalf("hashes = %d, want ~13", k)
	}
}

func TestFilterIsSafeForConcurrentUse(t *testing.T) {
	f := NewBloomFilter(1000, 0.001)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("addr-%d-%d", w, i)
				f.Add(key)
				if !f.MayContain(key) {
					t.Errorf("address %s lost", key)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
