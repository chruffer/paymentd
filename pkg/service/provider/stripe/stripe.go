package stripe

import (
	"time"
)

type Config struct {
	ProjectID int64
	MethodKey string
	Created   time.Time
	CreatedBy string

	SecretKey string
	PublicKey string
}

type Transaction struct {
	ProjectID  int64
	PaymentID  int64
	Timestamp  time.Time
	ChargeID   string
	TxID       string
	CreateTime int64
	Paid       bool
	CardToken  string
}
