package daemon

import (
	"testing"

	"github.com/BangBOOM/port-fwd-tools/internal/core"
)

func TestResolveFullIDCannotBeShadowedByAnotherName(t *testing.T) {
	target := "0123456789abcdef"
	m := &Manager{config: core.Config{Rules: []core.Rule{
		{ID: "fedcba9876543210", Name: target},
		{ID: target, Name: "database"},
	}}}
	got, err := m.resolve(target)
	if err != nil || got != target {
		t.Fatalf("full ID selected a different rule: %q %v", got, err)
	}
}
