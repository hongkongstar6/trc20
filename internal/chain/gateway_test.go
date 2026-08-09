package chain

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/tron"
)

// twoNodeGateway builds a gateway whose primary is `primary` and whose fallback
// is `fallback`, mirroring the FullNode-first / TronGrid-fallback deployment.
func twoNodeGateway(t *testing.T, primary, fallback http.Handler) (*Gateway, func()) {
	t.Helper()
	a := httptest.NewServer(primary)
	b := httptest.NewServer(fallback)
	gw, err := NewGateway(config.ChainConfig{
		Nodes: []config.NodeConfig{
			{Name: "fallback", Endpoint: b.URL, Priority: 2, Enabled: true, Timeout: "5s"},
			{Name: "primary", Endpoint: a.URL, Priority: 1, Enabled: true, Timeout: "5s"},
		},
		RetryPerNode: 1,
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return gw, func() { a.Close(); b.Close() }
}

func TestNewGatewayRequiresAnEnabledNode(t *testing.T) {
	_, err := NewGateway(config.ChainConfig{Nodes: []config.NodeConfig{
		{Name: "disabled", Endpoint: "http://127.0.0.1:1", Enabled: false},
	}})
	if !errors.Is(err, ErrNoNodeAvailable) {
		t.Fatalf("err = %v, want ErrNoNodeAvailable", err)
	}
}

// Queries must fail over to the next node by priority; a single node outage
// cannot be allowed to stall scanning.
func TestQueryFailsOverToSecondaryNode(t *testing.T) {
	var primaryHits, fallbackHits int32
	gw, closeAll := twoNodeGateway(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&primaryHits, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&fallbackHits, 1)
			_, _ = w.Write([]byte(`{"blockID":"aa","block_header":{"raw_data":{"number":42}}}`))
		}))
	defer closeAll()

	block, err := gw.GetNowBlock(context.Background())
	if err != nil {
		t.Fatalf("GetNowBlock: %v", err)
	}
	if block.Number() != 42 {
		t.Fatalf("number = %d, want 42", block.Number())
	}
	if primaryHits == 0 || fallbackHits == 0 {
		t.Fatalf("primary hits=%d fallback hits=%d, expected both to be tried", primaryHits, fallbackHits)
	}
}

// Every node down must surface as ErrNoNodeAvailable so the scanner stops
// instead of advancing its cursor past unscanned blocks.
func TestQueryReturnsErrNoNodeAvailableWhenAllFail(t *testing.T) {
	fail := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	gw, closeAll := twoNodeGateway(t, fail, fail)
	defer closeAll()

	if _, err := gw.GetNowBlock(context.Background()); !errors.Is(err, ErrNoNodeAvailable) {
		t.Fatalf("err = %v, want ErrNoNodeAvailable", err)
	}
}

// The fallback must receive byte-identical signed data. Re-signing or rebuilding
// on failover is how a double spend happens.
func TestBroadcastSendsIdenticalBytesToFallback(t *testing.T) {
	var primaryBody, fallbackBody string
	gw, closeAll := twoNodeGateway(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			primaryBody = string(raw)
			// Close without answering: the caller cannot know whether this node
			// accepted the transaction.
			hj, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			fallbackBody = string(raw)
			_, _ = w.Write([]byte(`{"result":true,"txid":"deadbeef"}`))
		}))
	defer closeAll()

	tx := &tron.Transaction{
		TxID:       "deadbeef",
		RawDataHex: "0a02aabb",
		Signature:  []string{"1234"},
	}
	res, err := gw.Broadcast(context.Background(), tx)
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if !res.Accepted {
		t.Fatal("the transaction should have been accepted by the fallback")
	}
	if primaryBody == "" || primaryBody != fallbackBody {
		t.Fatalf("fallback received different bytes:\nprimary:  %s\nfallback: %s", primaryBody, fallbackBody)
	}
}

// A node reporting a duplicate means our transaction is already in flight: that
// is success, not a failure to retry.
func TestBroadcastTreatsDuplicateAsAccepted(t *testing.T) {
	dup := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":false,"code":"DUP_TRANSACTION_ERROR","message":"6475702074726" }`))
	})
	gw, closeAll := twoNodeGateway(t, dup, dup)
	defer closeAll()

	res, err := gw.Broadcast(context.Background(), &tron.Transaction{TxID: "aa", RawDataHex: "0a", Signature: []string{"01"}})
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if !res.Accepted || !res.Duplicated {
		t.Fatalf("res = %+v, want accepted and duplicated", res)
	}
}

// A deterministic rejection must not be retried against other nodes: they will
// all reject it too, and the caller needs the reason.
func TestBroadcastReturnsRejection(t *testing.T) {
	reject := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":false,"code":"SIGERROR","message":"validate signature error"}`))
	})
	gw, closeAll := twoNodeGateway(t, reject, reject)
	defer closeAll()

	res, err := gw.Broadcast(context.Background(), &tron.Transaction{TxID: "aa", RawDataHex: "0a", Signature: []string{"01"}})
	if err == nil {
		t.Fatal("expected a rejection error")
	}
	if res == nil || res.Code != "SIGERROR" {
		t.Fatalf("res = %+v, want the provider rejection code", res)
	}
}

func TestGetTxInfoByIDReturnsNilWhenNotOnChain(t *testing.T) {
	empty := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	gw, closeAll := twoNodeGateway(t, empty, empty)
	defer closeAll()

	info, err := gw.GetTxInfoByID(context.Background(), "aa")
	if err != nil {
		t.Fatalf("GetTxInfoByID: %v", err)
	}
	if info != nil {
		t.Fatalf("info = %+v, want nil for a transaction that is not on chain", info)
	}
}

func TestTxInfoSucceeded(t *testing.T) {
	cases := []struct {
		name    string
		result  string
		receipt string
		want    bool
	}{
		{"empty receipt is success", "", "", true},
		{"explicit success", "SUCCESS", "SUCCESS", true},
		{"out of energy", "", "OUT_OF_ENERGY", false},
		{"reverted", "", "REVERT", false},
		{"failed result", "FAILED", "SUCCESS", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var info TxInfo
			info.Result = c.result
			info.Receipt.Result = c.receipt
			if got := info.Succeeded(); got != c.want {
				t.Fatalf("Succeeded() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAvailableEnergyNeverNegative(t *testing.T) {
	res := &AccountResource{EnergyLimit: 1000, EnergyUsed: 4000}
	if got := res.AvailableEnergy(); got != 0 {
		t.Fatalf("AvailableEnergy() = %d, want 0", got)
	}
	res = &AccountResource{EnergyLimit: 65000, EnergyUsed: 5000}
	if got := res.AvailableEnergy(); got != 60000 {
		t.Fatalf("AvailableEnergy() = %d, want 60000", got)
	}
}

func TestGetChainParametersFallsBackToDefaults(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A node that does not report getEnergyFee must not zero out the price
		// and make the break-even calculation think energy is free.
		_, _ = w.Write([]byte(`{"chainParameter":[{"key":"getMaxCpuTimeOfOneTx","value":80}]}`))
	})
	gw, closeAll := twoNodeGateway(t, h, h)
	defer closeAll()

	p, err := gw.GetChainParameters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.EnergyFeeSun != 100 || p.TransactionFeeSun != 1000 {
		t.Fatalf("params = %+v, want the documented defaults", p)
	}
}

func TestGetChainParametersReadsLivePrices(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"chainParameter":[{"key":"getEnergyFee","value":420},{"key":"getTransactionFee","value":1000}]}`))
	})
	gw, closeAll := twoNodeGateway(t, h, h)
	defer closeAll()

	p, err := gw.GetChainParameters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.EnergyFeeSun != 420 {
		t.Fatalf("EnergyFeeSun = %d, want 420", p.EnergyFeeSun)
	}
}

func TestDecodeHexMessage(t *testing.T) {
	// TRON returns error messages hex encoded.
	if got := decodeHexMessage("6475702074726 "); got == "" {
		t.Fatal("a non decodable message must be returned as is")
	}
	if got := decodeHexMessage("647570207472616e73616374696f6e"); got != "dup transaction" {
		t.Fatalf("got %q, want %q", got, "dup transaction")
	}
	if got := decodeHexMessage(""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestIsDuplicate(t *testing.T) {
	if !isDuplicate("DUP_TRANSACTION_ERROR", "") {
		t.Fatal("the duplicate code must be recognised")
	}
	if !isDuplicate("", "Transaction already exists") {
		t.Fatal("the already exists message must be recognised")
	}
	if isDuplicate("SIGERROR", "validate signature error") {
		t.Fatal("a signature error is not a duplicate")
	}
}

func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("限", 100) // 3 bytes per rune, so 200 lands mid rune
	got := truncate(s, 200)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated body is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected ellipsis: %q", got)
	}
}
