package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pgodlews/verity-api/core/plugin"
)

// pluginTrust asks the user once per project plugins file before the desktop
// app runs any plugin from it, and remembers approvals in app-local state
// (the user config directory), never in the project. A changed file, moved
// file, or changed plugin binary path asks again.
type pluginTrust struct {
	// store holds remembered approvals.
	store *plugin.TrustStore
	// confirm asks the user; it returns false when no UI is available.
	confirm func(plugin.Fingerprint) (bool, error)

	// mu serializes prompts so two parallel requests do not ask twice.
	mu sync.Mutex
}

// trust implements plugin.TrustFunc.
func (t *pluginTrust) trust(fp plugin.Fingerprint) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok, err := t.store.Trusted(fp); err != nil || ok {
		return ok, err
	}
	if t.confirm == nil {
		return false, nil
	}
	ok, err := t.confirm(fp)
	if err != nil || !ok {
		return false, err
	}
	return true, t.store.Remember(fp)
}

// trustStorePath is the app-local file of remembered plugin approvals.
func trustStorePath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "" // in-memory only
	}
	return filepath.Join(dir, "verity", "trusted-plugins.json")
}

// confirmPlugins shows a native Yes/No dialog naming the plugins file and the
// executables it would start. Without a running window it refuses.
func (a *App) confirmPlugins(fp plugin.Fingerprint) (bool, error) {
	if a.ctx == nil {
		return false, nil
	}
	bins := "(none)"
	if len(fp.Binaries) > 0 {
		bins = strings.Join(fp.Binaries, "\n")
	}
	choice, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:  runtime.QuestionDialog,
		Title: "Run project plugins?",
		Message: fmt.Sprintf("This project lists plugins in\n%s\n\nRunning it starts these programs on this computer:\n%s\n\n"+
			"Only continue if you trust this project. Verity remembers your choice until the file changes.",
			fp.Path, bins),
		Buttons:       []string{"Yes", "No"},
		DefaultButton: "No",
		CancelButton:  "No",
	})
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(choice), "yes"), nil
}
