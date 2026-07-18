package hctx

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/ddworken/hishtory/client/data"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// useTempHome points os.UserHomeDir() at a fresh temp dir so config/state read/writes are isolated
// and don't depend on (or touch) the real home directory.
func useTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	require.NoError(t, MakeHishtoryDir())
	return tmp
}

func hishtoryDir(home string) string {
	return path.Join(home, data.GetHishtoryPath())
}

func TestSetConfigSplitsIntoTwoFiles(t *testing.T) {
	home := useTempHome(t)

	config := ClientConfig{}
	config.UserSecret = "super-secret-value"
	config.DeviceId = "device-123"
	config.IsEnabled = true
	config.LastSavedHistoryLine = "  42  echo hi"
	config.ControlRSearchEnabled = true
	config.DisplayedColumns = []string{"Hostname", "Command"}
	require.NoError(t, SetConfig(&config))

	configBytes, err := os.ReadFile(path.Join(hishtoryDir(home), data.CONFIG_PATH))
	require.NoError(t, err)
	stateBytes, err := os.ReadFile(path.Join(hishtoryDir(home), data.STATE_PATH))
	require.NoError(t, err)

	// The config file must be pretty-printed and must NOT contain any secret/volatile state.
	require.Contains(t, string(configBytes), "\n  ", "config.json should be pretty-printed")
	require.Contains(t, string(configBytes), "enable_control_r_search")
	require.NotContains(t, string(configBytes), "super-secret-value")
	require.NotContains(t, string(configBytes), "user_secret")
	require.NotContains(t, string(configBytes), "last_saved_history_line")

	// The state file must hold the secret + volatile state, and nothing config-y.
	require.Contains(t, string(stateBytes), "super-secret-value")
	require.Contains(t, string(stateBytes), "device-123")
	require.Contains(t, string(stateBytes), "last_saved_history_line")
	require.NotContains(t, string(stateBytes), "enable_control_r_search")
}

func TestGetConfigRoundTrip(t *testing.T) {
	useTempHome(t)

	orig := ClientConfig{}
	orig.UserSecret = "secret-xyz"
	orig.DeviceId = "dev-1"
	orig.LastSavedHistoryLine = "line"
	orig.ControlRSearchEnabled = true
	orig.DisplayedColumns = []string{"Command"}
	require.NoError(t, SetConfig(&orig))

	got, err := GetConfig()
	require.NoError(t, err)
	require.Equal(t, "secret-xyz", got.UserSecret)
	require.Equal(t, "dev-1", got.DeviceId)
	require.Equal(t, "line", got.LastSavedHistoryLine)
	require.True(t, got.ControlRSearchEnabled)
	require.Equal(t, []string{"Command"}, got.DisplayedColumns)
}

func TestGetConfigToleratesMissingStateFile(t *testing.T) {
	home := useTempHome(t)

	config := ClientConfig{}
	config.UserSecret = "secret"
	config.ControlRSearchEnabled = true
	require.NoError(t, SetConfig(&config))

	// Simulate a config.json checked out on a fresh machine with no state file yet.
	require.NoError(t, os.Remove(path.Join(hishtoryDir(home), data.STATE_PATH)))

	got, err := GetConfig()
	require.NoError(t, err)
	require.Equal(t, "", got.UserSecret, "state should be zero-valued when state.json is absent")
	require.True(t, got.ControlRSearchEnabled, "config should still load")
}

func TestYamlExportDoesNotLeakSecret(t *testing.T) {
	// `hishtory status --config` yaml-marshals the config; the embedded state must be excluded.
	config := ClientConfig{}
	config.UserSecret = "top-secret"
	config.DeviceId = "dev"
	config.ControlRSearchEnabled = true

	y, err := yaml.Marshal(config)
	require.NoError(t, err)
	require.NotContains(t, string(y), "top-secret")
	require.NotContains(t, string(y), "dev")
	require.Contains(t, strings.ToLower(string(y)), "controlrsearchenabled")
}

func TestMigrationFromLegacyConfig(t *testing.T) {
	home := useTempHome(t)
	dir := hishtoryDir(home)

	// Write a pre-split combined config file with both config and state fields.
	legacy := map[string]any{
		"user_secret":            "legacy-secret",
		"device_id":              "legacy-device",
		"is_enabled":             true,
		"last_saved_history_line": "  99  ls",
		"enable_control_r_search": true,
		"displayed_columns":       []string{"Hostname", "Command"},
		"is_offline":              true,
	}
	legacyBytes, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path.Join(dir, data.LEGACY_CONFIG_PATH), legacyBytes, 0o644))

	// Loading config should trigger migration.
	got, err := GetConfig()
	require.NoError(t, err)
	require.Equal(t, "legacy-secret", got.UserSecret)
	require.Equal(t, "legacy-device", got.DeviceId)
	require.Equal(t, "  99  ls", got.LastSavedHistoryLine)
	require.True(t, got.ControlRSearchEnabled)
	require.True(t, got.IsOffline)

	// The split files must now exist...
	require.FileExists(t, path.Join(dir, data.CONFIG_PATH))
	require.FileExists(t, path.Join(dir, data.STATE_PATH))
	// ...the legacy file must be renamed to .old...
	require.NoFileExists(t, path.Join(dir, data.LEGACY_CONFIG_PATH))
	require.FileExists(t, path.Join(dir, data.LEGACY_CONFIG_PATH+".old"))

	// ...and the config file must not contain the secret.
	configBytes, err := os.ReadFile(path.Join(dir, data.CONFIG_PATH))
	require.NoError(t, err)
	require.NotContains(t, string(configBytes), "legacy-secret")
}

// TestGetConfigContentsUsesLegacyBeforeMigration guards the upgrade-detection path: before migration,
// GetConfigContents must return the user's actual persisted (legacy) bytes so that substring checks
// like "was highlight_matches ever explicitly set?" see the real answer, not a freshly-defaulted
// config that always contains every key.
func TestGetConfigContentsUsesLegacyBeforeMigration(t *testing.T) {
	home := useTempHome(t)
	dir := hishtoryDir(home)

	// A legacy config that predates the highlight_matches option.
	legacy := `{"user_secret":"s","enable_control_r_search":true}`
	require.NoError(t, os.WriteFile(path.Join(dir, data.LEGACY_CONFIG_PATH), []byte(legacy), 0o644))

	contents, err := GetConfigContents()
	require.NoError(t, err)
	require.Equal(t, legacy, string(contents), "should return raw legacy bytes, not a defaulted config")
	require.NotContains(t, string(contents), "highlight_matches")

	// GetConfigContents must not have triggered migration.
	require.NoFileExists(t, path.Join(dir, data.CONFIG_PATH))
	require.FileExists(t, path.Join(dir, data.LEGACY_CONFIG_PATH))
}

func TestMigrationIsNoOpWhenSplitFilesExist(t *testing.T) {
	home := useTempHome(t)
	dir := hishtoryDir(home)

	// A valid split config already exists.
	config := ClientConfig{}
	config.UserSecret = "current-secret"
	require.NoError(t, SetConfig(&config))

	// A stale legacy file should be ignored (not migrated over the current split files).
	require.NoError(t, os.WriteFile(path.Join(dir, data.LEGACY_CONFIG_PATH), []byte(`{"user_secret":"stale"}`), 0o644))

	got, err := GetConfig()
	require.NoError(t, err)
	require.Equal(t, "current-secret", got.UserSecret)
	// The legacy file must be left untouched (not renamed) since no migration happened.
	require.FileExists(t, path.Join(dir, data.LEGACY_CONFIG_PATH))
	require.NoFileExists(t, path.Join(dir, data.LEGACY_CONFIG_PATH+".old"))
}
