package bloom

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hongkongstar6/trc20/internal/config"
)

// The push is the only path that makes a brand new address matchable inside the
// sync interval, so it is exercised over a real socket.
func TestNotifyAddsTheAddressToTheServedFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	addr := freeAddr(t)
	config.Cfg = &config.Config{}
	config.Cfg.Bloom = config.BloomConfig{
		ExpectedAddresses: 1000,
		FalsePositiveRate: 0.0001,
		Listen:            addr,
		BloomNotifyURL:    "http://" + addr + addressPath,
		Token:             "t0ken",
		NotifyTimeout:     "3s",
	}
	r := GetNew(1000, 0.0001) // &bloom.Registry{filter: bloom.NewBloomFilter(1000, 0.0001)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := Serve(ctx); err != nil {
			t.Error("serve:", err)
		}
	}()
	waitUp(t, addr)

	const address = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	if r.MayContain(address) {
		t.Fatal("the address must be unknown before the push")
	}
	if err := Notify(ctx, address); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !r.MayContain(address) {
		t.Fatal("a pushed address must be matched immediately")
	}

	// A wrong token must not be able to poison the filter.
	config.Cfg.Bloom.Token = "wrong"
	if err := Notify(ctx, "TVjsyZ7fYF3qLF6BQgPmTEZy1xrNNyVAAA"); err == nil {
		t.Fatal("a push with a bad token must be rejected")
	}
}

func TestNotifyIsANoOpWithoutURL(t *testing.T) {
	config.Cfg = &config.Config{}
	if err := Notify(context.Background(), "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"); err != nil {
		t.Fatalf("notify without notify_url must be a no-op, got %v", err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitUp(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not start on %s", addr)
}
