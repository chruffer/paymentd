package provider

import (
	"database/sql"
	"errors"
)

var (
	ErrProviderNotFound = errors.New("provider not found")
)

const selectProvider = `
SELECT
	id,
	name
FROM provider
`

const selectProviderByID = selectProvider + `
WHERE
	id = ?
`

func scanSingleRow(row *sql.Row) (Provider, error) {
	p := Provider{}
	err := row.Scan(&p.ID, &p.Name)
	if err != nil {
		if err == sql.ErrNoRows {
			return p, ErrProviderNotFound
		}
		return p, err
	}
	return p, nil
}

func ProviderAllDB(db *sql.DB) ([]Provider, error) {
	rows, err := db.Query(selectProvider)

	d := make([]Provider, 0, 200)

	for rows.Next() {
		pr := Provider{}
		err := rows.Scan(&pr.ID, &pr.Name)
		if err != nil {
			return d, err
		}
		d = append(d, pr)
	}

	return d, err
}

func ProviderByIDDB(db *sql.DB, id int64) (Provider, error) {
	row := db.QueryRow(selectProviderByID, id)
	return scanSingleRow(row)
}

func ProviderByIDTx(db *sql.Tx, id int64) (Provider, error) {
	row := db.QueryRow(selectProviderByID, id)
	return scanSingleRow(row)
}