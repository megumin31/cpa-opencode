package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// verifyDisableEnable exercises a second native init of the same library file.
// Go shared objects can retain package state after dlclose (ELF NODELETE), so a
// new engine must be constructed on each successful init after native shutdown.
func verifyDisableEnable(ctx context.Context, host *pluginhost.Host, hostConfig *config.Config, upstream *mockUpstream, release releaseMetadata, rawAuth []byte) error {
	filename := filepath.Join(hostConfig.Plugins.Dir, release.Filename)
	before, err := os.Stat(filename)
	if err != nil {
		return err
	}
	disabled := false
	disabledConfig := *hostConfig
	disabledConfig.Plugins = hostConfig.Plugins
	disabledConfig.Plugins.Configs = map[string]config.PluginInstanceConfig{
		pluginID: {Enabled: &disabled, Raw: hostConfig.Plugins.Configs[pluginID].Raw},
	}
	stopped := make(chan struct{})
	go func() { host.ApplyConfig(ctx, &disabledConfig); close(stopped) }()
	if err := await(stopped, "idle per-plugin disable before same-file re-enable"); err != nil {
		return err
	}
	if len(host.RegisteredPlugins()) != 0 || host.HasAuthProvider(pluginID) || host.PluginBusy(pluginID) {
		return fmt.Errorf("idle plugin remained registered or busy after disable")
	}
	if ctx.Err() != nil {
		return fmt.Errorf("caller context canceled before same-file re-enable")
	}

	started := make(chan struct{})
	go func() { host.ApplyConfig(ctx, hostConfig); close(started) }()
	if err := await(started, "same-file native init and registration after disable"); err != nil {
		return err
	}
	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 || plugins[0].ID != pluginID || plugins[0].Metadata.Version != release.Version || !host.HasAuthProvider(pluginID) {
		return fmt.Errorf("same-file re-enable did not register a fresh engine: %+v", plugins)
	}
	after, err := os.Stat(filename)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("disable/re-enable changed the native library file: %v", err)
	}
	auth, handled, err := host.ParseAuth(ctx, pluginapi.AuthParseRequest{Provider: pluginID, FileName: "fresh.json", RawJSON: rawAuth})
	if err != nil || !handled || auth == nil || auth.Provider != pluginID {
		return fmt.Errorf("auth after same-file re-enable failed: handled=%v err=%v", handled, err)
	}
	models := host.ModelsForAuth(ctx, auth)
	if models.Err != nil || !models.Handled || len(models.Models) != 1 || models.Models[0].ID != modelID {
		return fmt.Errorf("discovery after same-file re-enable failed: %+v", models)
	}
	manager := &executorRegistry{}
	host.RegisterExecutors(manager, nil)
	executor, ok := manager.Executor(pluginID)
	if !ok {
		return fmt.Errorf("executor missing after same-file re-enable")
	}
	if err := verifyFreshRequests(ctx, host, executor, auth, upstream); err != nil {
		return err
	}
	if upstream.configCalls.Load() != 4 || upstream.inferenceCalls.Load() != 10 {
		return fmt.Errorf("unexpected re-enable mock calls: discovery=%d inference=%d", upstream.configCalls.Load(), upstream.inferenceCalls.Load())
	}
	select {
	case err := <-upstream.failures:
		return err
	default:
	}
	finished := make(chan struct{})
	go func() { host.ShutdownAll(); close(finished) }()
	if err := await(finished, "shutdown after same-file re-enable and fresh requests"); err != nil {
		return err
	}
	if len(host.RegisteredPlugins()) != 0 || host.PluginBusy(pluginID) || ctx.Err() != nil {
		return fmt.Errorf("same-file re-enabled engine did not shut down cleanly with caller context alive")
	}
	fmt.Println("PASS: idle disable/re-enable reinitializes the exact same native library, restores auth/discovery, executes fresh aggregate/stream requests and unloads cleanly")
	return nil
}
