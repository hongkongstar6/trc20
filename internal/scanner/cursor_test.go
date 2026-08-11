package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
)

// blockHash is the deterministic hash the fake node returns for a height, so a
// test can build both a matching and a foreign cursor.
func blockHash(num int64) string { return fmt.Sprintf("%064x", num) }

// fakeNodeGateway serves getblockbynum and gettransactioninfobyblocknum from a
// single deterministic chain.
func fakeNodeGateway(t *testing.T) *chain.Gateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Num int64 `json:"num"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getblockbynum"):
			fmt.Fprintf(w, `{"blockID":%q,"block_header":{"raw_data":{"number":%d,"timestamp":1,"parentHash":%q}}}`,
				blockHash(body.Num), body.Num, blockHash(body.Num-1))
		case strings.HasSuffix(r.URL.Path, "/gettransactioninfobyblocknum"):
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	gw, err := chain.NewGateway(config.ChainConfig{
		ChainNodes:   []config.NodeConfig{{Name: "fake", Endpoint: srv.URL, Enabled: true, Timeout: "5s"}},
		RetryPerNode: 1,
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return gw
}

func TestCursorNameIsPerNetwork(t *testing.T) {
	config.Cfg = &config.Config{Network: "mainnet"}
	if got := cursorName(); got != "tron_deposit:mainnet" {
		t.Fatalf("cursor name = %s", got)
	}
	config.Cfg = &config.Config{Network: "nile"}
	if got := cursorName(); got != "tron_deposit:nile" {
		t.Fatalf("cursor name = %s", got)
	}
}

// A cursor written while the deployment pointed at another network is far below
// the head and stores a hash the chain does not know: it must be rejected,
// otherwise the scanner crawls millions of historical blocks and never reaches
// the head.
func TestCursorOnChain(t *testing.T) {
	cfg := &config.Config{Network: "mainnet"}
	cfg.Deposit.ReorgDepth = 60
	config.Cfg = cfg
	s := &Scanner{gw: fakeNodeGateway(t)}
	const head = 85208467

	cases := []struct {
		name   string
		cursor model.ChainCursor
		want   bool
	}{
		{"same chain", model.ChainCursor{BlockNumber: head - 1000, BlockHash: blockHash(head - 1000)}, true},
		{"foreign chain", model.ChainCursor{BlockNumber: 69865408, BlockHash: "dead"}, false},
		{"above the head", model.ChainCursor{BlockNumber: head + 10, BlockHash: blockHash(head + 10)}, false},
		{"no hash yet", model.ChainCursor{BlockNumber: 69865408}, true},
		// Close to the head a hash mismatch is a reorg, which handleReorg owns.
		{"recent mismatch is a reorg", model.ChainCursor{BlockNumber: head - 10, BlockHash: "dead"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.cursorOnChain(context.Background(), &c.cursor, head)
			if err != nil {
				t.Fatalf("cursorOnChain: %v", err)
			}
			if got != c.want {
				t.Fatalf("cursorOnChain = %v, want %v", got, c.want)
			}
		})
	}
}

// Prefetching is what lets the scanner outrun the chain, but the blocks must
// still be applied in ascending order or reorg detection breaks.
func TestFetchRangeKeepsBlockOrder(t *testing.T) {
	//cfg := config.Config{}
	config.Cfg.Deposit.FetchConcurrency = 8
	s := &Scanner{gw: fakeNodeGateway(t)}

	const from, to = 1000, 1019
	got := s.fetchRange(context.Background(), from, to)
	if len(got) != to-from+1 {
		t.Fatalf("len = %d, want %d", len(got), to-from+1)
	}
	for i := range got {
		if got[i].err != nil {
			t.Fatalf("block %d: %v", from+int64(i), got[i].err)
		}
		if want := from + int64(i); got[i].block.Number() != want {
			t.Fatalf("position %d holds block %d, want %d", i, got[i].block.Number(), want)
		}
	}
}
