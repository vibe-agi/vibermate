package clientadapter

import (
	"os"
	"testing"
)

func TestStartsAProcess(t *testing.T) {
	_, _ = os.StartProcess("/bin/true", nil, &os.ProcAttr{})
}
