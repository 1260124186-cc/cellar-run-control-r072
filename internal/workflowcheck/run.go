package workflowcheck

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/archive"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/httpapi"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// checkHarness carries the real stores used by a workflow check so checks that
// exercise the historical repository can reach both directories.
type checkHarness struct {
	dataDir    string
	archiveDir string
	api        *client
}

func Run(name string) error {
	dataDir, err := os.MkdirTemp("", "cellar-run-check-*")
	if err != nil {
		return fmt.Errorf("create check directory: %w", err)
	}
	defer os.RemoveAll(dataDir)
	archiveDir := filepath.Join(dataDir, "archive")

	store, err := storage.Open(dataDir)
	if err != nil {
		return err
	}
	archiveStore, err := archive.Open(archiveDir)
	if err != nil {
		return err
	}
	app := service.New(store, clock.System{}).WithArchive(archiveStore)
	server := httptest.NewServer(httpapi.NewHandler(app))
	defer server.Close()
	harness := checkHarness{
		dataDir:    dataDir,
		archiveDir: archiveDir,
		api:        newClient(server.URL),
	}

	switch name {
	case "formula-approval":
		if err := formulaApproval(harness.api); err != nil {
			return err
		}
	case "vessel-run-lifecycle":
		if err := vesselRunLifecycle(harness.api); err != nil {
			return err
		}
	case "observations-completion":
		if err := observationsCompletion(harness.api); err != nil {
			return err
		}
	case "archive-restore":
		if err := archiveRestore(harness); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown workflow check %q", name)
	}
	fmt.Printf("workflow check passed: %s\n", name)
	return nil
}
