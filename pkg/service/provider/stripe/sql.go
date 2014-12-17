package stripe

import (
	"database/sql"
	"errors"
	"github.com/fritzpay/paymentd/pkg/paymentd/payment_method"
)

var (
	ErrConfigNotFound      = errors.New("config not found")
	ErrTransactionNotFound = errors.New("transaction not found")
)

const selectConfig = `
SELECT
	c.project_id,
	c.method_key,
	c.created,
	c.created_by,
	c.secret_key,
	c.public_key
FROM provider_stripe_config AS c
`
const selectConfigByProjectIDAndMethodKey = selectConfig + `
WHERE
	c.project_id = ?
	AND
	c.method_key = ?
	AND
	c.created = (
		SELECT MAX(created) FROM provider_stripe_config
		WHERE
			project_id = c.project_id
			AND
			method_key = c.method_key
	)
`

func scanConfig(row *sql.Row) (*Config, error) {
	cfg := &Config{}
	err := row.Scan(
		&cfg.ProjectID,
		&cfg.MethodKey,
		&cfg.Created,
		&cfg.CreatedBy,
		&cfg.SecretKey,
		&cfg.PublicKey,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return cfg, ErrConfigNotFound
		}
		return cfg, err
	}
	return cfg, nil
}

func ConfigByPaymentMethodTx(db *sql.Tx, method *payment_method.Method) (*Config, error) {
	row := db.QueryRow(selectConfigByProjectIDAndMethodKey, method.ProjectID, method.MethodKey)
	return scanConfig(row)
}

func ConfigByPaymentMethodDB(db *sql.DB, method *payment_method.Method) (*Config, error) {
	row := db.QueryRow(selectConfigByProjectIDAndMethodKey, method.ProjectID, method.MethodKey)
	return scanConfig(row)
}

const insertConfig = `
INSERT INTO provider_stripe_config
(project_id, method_key, created, created_by, secret_key, public_key)
VALUES
(?, ?, ?, ?, ?, ?)
`

func doInsertConfig(stmt *sql.Stmt, c *Config) error {
	_, err := stmt.Exec(
		c.ProjectID,
		c.MethodKey,
		c.Created,
		c.CreatedBy,
		c.SecretKey,
		c.PublicKey,
	)
	stmt.Close()
	return err
}

const insertTransaction = `
INSERT INTO provider_stripe_transaction
(project_id, payment_id, timestamp, stripe_charge_id, stripe_tx, stripe_create_time, stripe_paid, stripe_card_token)
VALUES
(?, ?, ?, ?, ?, ?, ?, ?)
`

func InsertConfigTx(db *sql.Tx, c *Config) error {
	stmt, err := db.Prepare(insertConfig)
	if err != nil {
		return err
	}
	return doInsertConfig(stmt, c)
}

func doInsertTransaction(stmt *sql.Stmt, t *Transaction) error {
	_, err := stmt.Exec(
		t.ProjectID,
		t.PaymentID,
		t.Timestamp.UnixNano(),
		t.ChargeID,
		t.TxID,
		t.CreateTime,
		t.Paid,
		t.CardToken,
	)
	stmt.Close()
	return err
}

func InsertTransactionTx(db *sql.Tx, t *Transaction) error {
	stmt, err := db.Prepare(insertTransaction)
	if err != nil {
		return err
	}
	return doInsertTransaction(stmt, t)
}

func InsertTransactionDB(db *sql.DB, t *Transaction) error {
	stmt, err := db.Prepare(insertTransaction)
	if err != nil {
		return err
	}
	return doInsertTransaction(stmt, t)
}
