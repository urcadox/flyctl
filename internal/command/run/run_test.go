package run

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuoting(t *testing.T) {
	assert.Equal(t, quote("bash"), "'bash'")
	assert.Equal(t, quote("bash -x"), "'bash -x'")
	assert.Equal(t, quote("'bash'"), "\\''bash'\\'")
	assert.Equal(t, quote("\"bash\""), "'\"bash\"'")
	assert.Equal(t, quote("'multiple' 'single quotes'"), "\\''multiple'\\'' '\\''single quotes'\\'")
}
