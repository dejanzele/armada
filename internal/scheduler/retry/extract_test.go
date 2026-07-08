package retry

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/armadaproject/armada/pkg/armadaevents"
)

func TestExtractCategory(t *testing.T) {
	tests := map[string]struct {
		err      *armadaevents.Error
		expected string
	}{
		"nil error":    {err: nil, expected: ""},
		"category set": {err: &armadaevents.Error{FailureCategory: "infrastructure"}, expected: "infrastructure"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, extractCategory(tc.err))
		})
	}
}

func TestExtractSubcategory(t *testing.T) {
	tests := map[string]struct {
		err      *armadaevents.Error
		expected string
	}{
		"nil error":       {err: nil, expected: ""},
		"subcategory set": {err: &armadaevents.Error{FailureSubcategory: "oom"}, expected: "oom"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, extractSubcategory(tc.err))
		})
	}
}
