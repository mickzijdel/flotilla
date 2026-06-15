package fleet

import "github.com/mickzijdel/flotilla/internal/backend"

type Fleet struct {
	Backend   backend.Backend
	BaseImage string
}
