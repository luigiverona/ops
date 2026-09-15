package inspect

import (
	"github.com/luigiverona/ops/internal/testpkg"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testpkg.IsolateFlatpakTests(m)) }
