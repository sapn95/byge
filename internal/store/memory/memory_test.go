package memory

import (
	"testing"

	"byge/internal/store"
	"byge/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return New() })
}
