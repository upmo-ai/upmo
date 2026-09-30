package artifacts

// Record is one artifact row a person may see under row security.
type Record struct {
	ID    string
	Task  string
	Name  string
	Store string
	Key   string
}
