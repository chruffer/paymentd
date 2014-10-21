package principal

import (
	"time"
)

// Principal represents a principal
//
// A principal is a resource under which projects are organized
type Principal struct {
	ID        int64
	Created   time.Time
	CreatedBy string
	Name      string

	Metadata map[string]string
}

// Empty returns true if the principal is considered empty/unitialized
func (p Principal) Empty() bool {
	return p.ID == 0 && p.Name == ""
}