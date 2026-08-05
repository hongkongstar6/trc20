package scanner

import "github.com/hongkongstar6/trc20/internal/model"

func depositRecordFixture() model.DepositRecord {
	return model.DepositRecord{TxID: "abc123", EventIndex: 2}
}
