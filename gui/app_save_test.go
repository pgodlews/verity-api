package main

import (
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
	"github.com/pgodlews/verity-api/core/workspace"
)

func TestDesktopSaveRejectsEmptyBaseline(t *testing.T) {
	app := &App{}
	_, err := app.SaveStep(workspace.SaveStepRequest{Path: "project.yaml"})
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeValidation {
		t.Fatalf("SaveStep: %v", err)
	}
	_, err = app.SaveEnvironment(workspace.SaveEnvironmentRequest{Path: "project.yaml", Name: "local"})
	ve, ok = verr.As(err)
	if !ok || ve.Code != verr.CodeValidation {
		t.Fatalf("SaveEnvironment: %v", err)
	}
}

func TestDesktopSaveAcceptsFileBaseline(t *testing.T) {
	app := &App{}
	_, err := app.SaveStep(workspace.SaveStepRequest{Path: "missing.yaml", FileBaseline: "abc"})
	if ve, ok := verr.As(err); ok && ve.Code == verr.CodeValidation {
		t.Fatalf("file baseline should satisfy the desktop check: %v", err)
	}
	if err == nil {
		t.Fatal("missing project was saved")
	}
}
