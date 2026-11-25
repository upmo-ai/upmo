package postgres

// SpendUpdateSQL is the production spend write. Tests run this statement directly.
func SpendUpdateSQL() string { return spendUpdateSQL }

// RecordUpdateSQL is the production passes write. Tests run this statement directly.
func RecordUpdateSQL() string { return recordUpdateSQL }
