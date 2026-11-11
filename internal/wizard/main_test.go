package wizard

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/mysql"
)

func TestMain(m *testing.M) {
	defaultListDatabases = func(context.Context, mysql.ToolInfo, config.Database, string) ([]string, error) {
		return nil, errors.New("database listing is disabled in tests")
	}
	os.Exit(m.Run())
}
