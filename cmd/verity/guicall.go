package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/workspace"
)

// cmdGUICall is an internal command used by the GUI's browser dev bridge
// (gui/frontend/vite.bridge.ts): it calls one desktop-app method by name with
// a JSON array of arguments on stdin and prints {"ok":true,"value":…} or
// {"ok":false,"error":"…"}. The desktop app calls the same core/workspace
// functions directly, so both behave the same. Not part of the public CLI.
func cmdGUICall(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "gui-call: usage: verity gui-call <Method> < args.json")
		return exitUsage
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return guiReply(nil, err)
	}
	var params []json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return guiReply(nil, fmt.Errorf("arguments must be a JSON array: %w", err))
		}
	}
	arg := func(i int, v any) error {
		if i >= len(params) {
			return fmt.Errorf("%s: missing argument %d", args[0], i+1)
		}
		return json.Unmarshal(params[i], v)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "OAuthLogin", "OAuthLogout", "OAuthStatus":
		return guiReply(nil, fmt.Errorf("OAuth sessions require the desktop app; the browser dev bridge uses a separate process per call"))
	case "Catalog":
		return guiReply(workspace.Catalog(), nil)
	case "ValidateProject":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		env := ""
		if len(params) > 1 {
			if err := arg(1, &env); err != nil {
				return guiReply(nil, err)
			}
		}
		return guiReply(workspace.Validate(ctx, path, env, nil), nil)
	case "GraphQLTemplates":
		var req workspace.GraphQLTemplateRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.GraphQLTemplates(ctx, req, nil))
	case "InspectProject":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Inspect(path))
	case "RunProject":
		var opts workspace.RunOptions
		if err := arg(0, &opts); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Run(ctx, opts))
	case "StartLoad":
		var opts workspace.LoadOptions
		if err := arg(0, &opts); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Load(ctx, opts))
	case "LoadTargets":
		var opts workspace.LoadOptions
		if err := arg(0, &opts); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.LoadTargets(ctx, opts, nil, plugin.TrustFromEnv))
	case "SaveLoadReport":
		var req workspace.SaveLoadReportRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.SaveLoadReport(req))
	case "CancelRun", "CancelLoad":
		return guiReply(nil, fmt.Errorf("%s requires the desktop app; the browser dev bridge runs one call at a time", args[0]))
	case "SaveStep":
		var req workspace.SaveStepRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.SaveStep(req))
	case "CheckStep":
		var step model.Step
		if err := arg(0, &step); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.CheckStep(step))
	case "KeychainSecretExists":
		var name string
		if err := arg(0, &name); err != nil {
			return guiReply(nil, err)
		}
		return guiReply((&secrets.Keychain{}).Exists(name))
	case "StoreKeychainSecret":
		var name, value string
		if err := arg(0, &name); err != nil {
			return guiReply(nil, err)
		}
		if err := arg(1, &value); err != nil {
			return guiReply(nil, err)
		}
		if err := (&secrets.Keychain{}).Set(name, value); err != nil {
			return guiReply(nil, err)
		}
		return guiReply("${secret:keychain:"+name+"}", nil)
	case "SaveEnvironment":
		var req workspace.SaveEnvironmentRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.SaveEnvironment(req))
	case "ImportPreview":
		var kind, spec string
		var envs []string
		if err := arg(0, &kind); err != nil {
			return guiReply(nil, err)
		}
		if err := arg(1, &spec); err != nil {
			return guiReply(nil, err)
		}
		if len(params) > 2 {
			if err := arg(2, &envs); err != nil {
				return guiReply(nil, err)
			}
		}
		return guiReply(workspace.Preview(kind, spec, envs))
	case "ImportSave":
		var req workspace.ImportSaveRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Save(req))
	case "PickFile":
		return guiReply(nil, fmt.Errorf("file dialogs are only available in the desktop app"))
	case "GitStatus":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.GitStatus(path))
	case "GitChanges":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.GitChanges(path))
	case "GitStashes":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Stashes(ctx, path))
	case "GitStash":
		var req workspace.StashRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Stash(ctx, req))
	case "GitStashAction", "GitStashChanges":
		var req workspace.StashActionRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		if args[0] == "GitStashChanges" {
			return guiReply(workspace.StashChanges(ctx, req))
		}
		return guiReply(workspace.StashAction(ctx, req))
	case "GitCommit":
		var req workspace.CommitRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Commit(ctx, req))
	case "GitPull":
		var req workspace.PullRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Pull(ctx, req, nil))
	case "GitFetch":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Fetch(ctx, path, nil))
	case "GitPush":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Push(ctx, path, nil))
	case "GitPushUpstream":
		var req workspace.UpstreamRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.PushUpstream(ctx, req, nil))
	case "GitBranches":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Branches(ctx, path))
	case "GitSwitch":
		var req workspace.SwitchRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.Switch(ctx, req))
	case "GitCreateBranch":
		var req workspace.CreateBranchRequest
		if err := arg(0, &req); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.CreateBranch(ctx, req))
	case "GitMergeAbort":
		var path string
		if err := arg(0, &path); err != nil {
			return guiReply(nil, err)
		}
		return guiReply(workspace.MergeAbort(ctx, path, nil))
	case "OpenFile":
		return guiReply(nil, fmt.Errorf("opening a file is only available in the desktop app"))
	case "CancelGit":
		return guiReply(nil, fmt.Errorf("CancelGit requires the desktop app; the browser dev bridge runs one call at a time"))
	case "WatchProject", "UnwatchProject":
		return guiReply(nil, fmt.Errorf("%s requires the desktop app; the browser dev bridge does not watch files", args[0]))
	}
	return guiReply(nil, fmt.Errorf("unknown method %q", args[0]))
}

func guiReply(value any, err error) int {
	if err != nil {
		emitJSON(map[string]any{"ok": false, "error": err.Error()})
		return exitOK
	}
	emitJSON(map[string]any{"ok": true, "value": value})
	return exitOK
}
