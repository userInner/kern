package save

type Tx interface {
	Write(value string) error
	Commit() error
	Rollback() error
}

type DB interface {
	Begin() (Tx, error)
}

func Value(database DB, value string) error {
	transaction, err := database.Begin()
	if err != nil {
		return err
	}
	if err := transaction.Write(value); err != nil {
		return err
	}
	return transaction.Commit()
}
