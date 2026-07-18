package hctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"github.com/ddworken/hishtory/client/data"
	"github.com/ddworken/hishtory/client/tui/keybindings"
	"github.com/ddworken/hishtory/shared"

	// Needed to use sqlite without CGO
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	hishtoryLogger *logrus.Logger
	getLoggerOnce  sync.Once
)

func GetLogger() *logrus.Logger {
	getLoggerOnce.Do(func() {
		homedir, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Errorf("failed to get user's home directory: %w", err))
		}
		err = MakeHishtoryDir()
		if err != nil {
			panic(err)
		}

		lumberjackLogger := &lumberjack.Logger{
			Filename:   path.Join(homedir, data.GetHishtoryPath(), "hishtory.log"),
			MaxSize:    1, // MB
			MaxBackups: 1,
			MaxAge:     30, // days
		}

		logFormatter := new(logrus.TextFormatter)
		logFormatter.TimestampFormat = time.RFC3339
		logFormatter.FullTimestamp = true

		hishtoryLogger = logrus.New()
		hishtoryLogger.SetFormatter(logFormatter)
		hishtoryLogger.SetOutput(lumberjackLogger)

		// Configure the log level from the config file, if the config file exists
		hishtoryLogger.SetLevel(logrus.InfoLevel)
		cfg, err := GetConfig()
		if err == nil {
			hishtoryLogger.SetLevel(cfg.LogLevel)
		}
	})
	return hishtoryLogger
}

func MakeHishtoryDir() error {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user's home directory: %w", err)
	}
	err = os.MkdirAll(path.Join(homedir, data.GetHishtoryPath()), 0o744)
	if err != nil {
		return fmt.Errorf("failed to create ~/%s dir: %w", data.GetHishtoryPath(), err)
	}
	return nil
}

func OpenLocalSqliteDb() (*gorm.DB, error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user's home directory: %w", err)
	}
	err = MakeHishtoryDir()
	if err != nil {
		return nil, err
	}
	newLogger := logger.New(
		GetLogger().WithField("fromSQL", true),
		logger.Config{
			SlowThreshold:             100 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: false,
			Colorful:                  false,
		},
	)
	dbFilePath := path.Join(homedir, data.GetHishtoryPath(), data.DB_PATH)
	dsn := fmt.Sprintf("file:%s?mode=rwc&_journal_mode=WAL", dbFilePath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{SkipDefaultTransaction: true, Logger: newLogger})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the DB: %w", err)
	}
	tx, err := db.DB()
	if err != nil {
		return nil, err
	}
	err = tx.Ping()
	if err != nil {
		return nil, err
	}
	db.AutoMigrate(&data.HistoryEntry{})
	db.Exec("PRAGMA journal_mode = WAL")
	db.Exec("pragma mmap_size = 268435456")
	db.Exec("CREATE INDEX IF NOT EXISTS start_time_index ON history_entries(start_time)")
	db.Exec("CREATE INDEX IF NOT EXISTS end_time_index ON history_entries(end_time)")
	db.Exec("CREATE INDEX IF NOT EXISTS entry_id_index ON history_entries(entry_id)")
	return db, nil
}

type hishtoryContextKey string

const (
	ConfigCtxKey  hishtoryContextKey = "config"
	DbCtxKey      hishtoryContextKey = "db"
	HomedirCtxKey hishtoryContextKey = "homedir"
	BackendCtxKey hishtoryContextKey = "backend"
)

func MakeContext() context.Context {
	ctx := context.Background()
	config, err := GetConfig()
	if err != nil {
		panic(fmt.Errorf("failed to retrieve config: %w", err))
	}
	ctx = context.WithValue(ctx, ConfigCtxKey, &config)
	db, err := OpenLocalSqliteDb()
	if err != nil {
		panic(fmt.Errorf("failed to open local DB: %w", err))
	}
	ctx = context.WithValue(ctx, DbCtxKey, db)
	homedir, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Errorf("failed to get homedir: %w", err))
	}
	ctx = context.WithValue(ctx, HomedirCtxKey, homedir)
	return ctx
}

func GetConf(ctx context.Context) *ClientConfig {
	v := ctx.Value(ConfigCtxKey)
	if v != nil {
		return (v.(*ClientConfig))
	}
	panic(fmt.Errorf("failed to find config in ctx"))
}

func GetDb(ctx context.Context) *gorm.DB {
	v := ctx.Value(DbCtxKey)
	if v != nil {
		return v.(*gorm.DB)
	}
	panic(fmt.Errorf("failed to find db in ctx"))
}

func GetHome(ctx context.Context) string {
	v := ctx.Value(HomedirCtxKey)
	if v != nil {
		return v.(string)
	}
	panic(fmt.Errorf("failed to find homedir in ctx"))
}

// GetBackend returns the sync backend from context.
// Returns nil if no backend has been set (caller should handle this case).
func GetBackend(ctx context.Context) shared.SyncBackend {
	v := ctx.Value(BackendCtxKey)
	if v != nil {
		return v.(shared.SyncBackend)
	}
	return nil
}

// WithBackend returns a new context with the backend set.
func WithBackend(ctx context.Context, b shared.SyncBackend) context.Context {
	return context.WithValue(ctx, BackendCtxKey, b)
}

// ClientState holds per-installation and volatile state. It is persisted separately from
// ClientConfig (in STATE_PATH) so that the VCS-friendly config file stays stable across runs.
// It contains secrets (UserSecret) and bookkeeping that changes on nearly every command, so it
// must never be checked into version control.
type ClientState struct {
	// The user secret that is used to derive encryption keys for syncing history entries
	UserSecret string `json:"user_secret"`
	// Whether hishtory recording is enabled
	IsEnabled bool `json:"is_enabled"`
	// A device ID used to track which history entry came from which device for remote syncing
	DeviceId string `json:"device_id"`
	// Used for skipping history entries prefixed with a space in bash
	LastPreSavedHistoryLine string `json:"last_presaved_history_line"`
	// Used for skipping history entries prefixed with a space in bash
	LastSavedHistoryLine string `json:"last_saved_history_line"`
	// Used for uploading history entries that we failed to upload due to a missing network connection
	HaveMissedUploads     bool  `json:"have_missed_uploads"`
	MissedUploadTimestamp int64 `json:"missed_upload_timestamp"`
	// Used for uploading deletion requests that we failed to upload due to a missed network connection
	// Note that this is only applicable for deleting pre-saved entries. For interactive deletion, we just
	// show the user an error message if they're offline.
	PendingDeletionRequests []shared.DeletionRequest `json:"pending_deletion_requests"`
	// Used for avoiding double imports of .bash_history
	HaveCompletedInitialImport bool `json:"have_completed_initial_import"`
}

type ClientConfig struct {
	// ClientState holds per-installation/volatile state persisted to a separate file (STATE_PATH).
	// It is embedded so existing accessors (e.g. config.UserSecret) keep working via field promotion,
	// but tagged json:"-"/yaml:"-" so it is excluded when (de)serializing the config file and the
	// `hishtory status --config` yaml dump. State is persisted/loaded explicitly via STATE_PATH.
	ClientState `json:"-" yaml:"-"`

	// Backend configuration for syncing
	// BackendType specifies the sync backend: "http" (default) or "s3"
	BackendType string `json:"backend_type,omitempty"`
	// S3Config holds configuration for the S3 backend (only used when BackendType is "s3")
	S3Config *S3BackendConfig `json:"s3_config,omitempty"`
	// Whether control-r bindings are enabled
	ControlRSearchEnabled bool `json:"enable_control_r_search"`
	// The set of columns that the user wants to be displayed
	DisplayedColumns []string `json:"displayed_columns"`
	// Custom columns
	CustomColumns []CustomColumnDefinition `json:"custom_columns"`
	// Whether to force enable a compact mode for the TUI
	ForceCompactMode bool `json:"force_compact_mode"`
	// Whether this is an offline instance of hishtory with no syncing
	IsOffline bool `json:"is_offline"`
	// Whether duplicate commands should be displayed
	FilterDuplicateCommands bool `json:"filter_duplicate_commands"`
	// Whether to filter out commands that start with whitespace (space, tab, etc.)
	FilterWhitespacePrefix bool `json:"filter_whitespace_prefix,omitempty"`
	// A format string for the timestamp
	TimestampFormat string `json:"timestamp_format"`
	// Beta mode, enables unspecified additional beta features
	// Currently: This enables pre-saving of history entries to better handle long-running commands
	BetaMode bool `json:"beta_mode"`
	// Whether to highlight matches in search results
	HighlightMatches bool `json:"highlight_matches"`
	// Whether to enable AI completion
	AiCompletion bool `json:"ai_completion"`
	// Whether to enable presaving
	EnablePresaving bool `json:"enable_presaving"`
	// The current color scheme for the TUI
	ColorScheme ColorScheme `json:"color_scheme"`
	// A default filter that will be applied to all search queries
	DefaultFilter string `json:"default_filter"`
	// The endpoint to use for AI suggestions
	AiCompletionEndpoint string `json:"ai_completion_endpoint"`
	// Custom key bindings for the TUI
	KeyBindings keybindings.SerializableKeyMap `json:"key_bindings"`
	// The log level for hishtory (e.g., "debug", "info", "warn", "error")
	LogLevel logrus.Level `json:"log_level"`
	// Whether the TUI should render in full-screen mode
	FullScreenRendering bool `json:"full_screen_rendering"`
	// Columns that are used for default searches.
	// See https://github.com/ddworken/hishtory/issues/268 for context on this.
	DefaultSearchColumns []string `json:"default_search_columns"`
}

type ColorScheme struct {
	SelectedText       string
	SelectedBackground string
	BorderColor        string
}

type CustomColumnDefinition struct {
	ColumnName    string `json:"column_name"`
	ColumnCommand string `json:"column_command"`
}

// S3BackendConfig holds configuration for the S3 sync backend.
// This is stored in the client config file (except for SecretAccessKey).
type S3BackendConfig struct {
	// Bucket is the S3 bucket name (required)
	Bucket string `json:"bucket"`
	// Region is the AWS region (required)
	Region string `json:"region"`
	// Endpoint is a custom S3-compatible endpoint (optional, for MinIO, Backblaze, etc.)
	Endpoint string `json:"endpoint,omitempty"`
	// AccessKeyID is the AWS access key ID (optional if using IAM roles or env vars)
	AccessKeyID string `json:"access_key_id,omitempty"`
	// Prefix is an optional path prefix within the bucket (e.g., "hishtory/")
	Prefix string `json:"prefix,omitempty"`
}

// configFilePath returns the absolute path to the (post-split) config file.
func configFilePath() (string, error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve homedir: %w", err)
	}
	return path.Join(homedir, data.GetHishtoryPath(), data.CONFIG_PATH), nil
}

// stateFilePath returns the absolute path to the state file.
func stateFilePath() (string, error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve homedir: %w", err)
	}
	return path.Join(homedir, data.GetHishtoryPath(), data.STATE_PATH), nil
}

// legacyConfigFilePath returns the absolute path to the pre-split combined config file.
func legacyConfigFilePath() (string, error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve homedir: %w", err)
	}
	return path.Join(homedir, data.GetHishtoryPath(), data.LEGACY_CONFIG_PATH), nil
}

// GetConfigContents returns the raw bytes of the user's persisted config. If the split config file
// exists it is returned; otherwise, for a not-yet-migrated install, the legacy combined config file
// is returned. This deliberately does NOT trigger migration and does NOT include the state file, so
// that upgrade-detection logic (which checks whether a given key was ever explicitly persisted by the
// user) inspects exactly what the user last wrote rather than a freshly-defaulted config.
func GetConfigContents() ([]byte, error) {
	configPath, err := configFilePath()
	if err != nil {
		return nil, err
	}
	dat, err := os.ReadFile(configPath)
	if err == nil {
		return dat, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		// The split config file doesn't exist yet; fall back to the legacy combined file if present.
		legacyPath, lerr := legacyConfigFilePath()
		if lerr != nil {
			return nil, lerr
		}
		if legacyDat, lerr := os.ReadFile(legacyPath); lerr == nil {
			return legacyDat, nil
		}
	}
	homedir, herr := os.UserHomeDir()
	if herr != nil {
		return nil, fmt.Errorf("failed to read config file (and failed to get homedir too): %w", err)
	}
	files, lerr := os.ReadDir(path.Join(homedir, data.GetHishtoryPath()))
	if lerr != nil {
		return nil, fmt.Errorf("failed to read config file (and failed to list too): %w", err)
	}
	filenames := ""
	for _, file := range files {
		filenames += file.Name()
		filenames += ", "
	}
	return nil, fmt.Errorf("failed to read config file (files in HISHTORY_PATH: %s): %w", filenames, err)
}

// maybeMigrateLegacyConfig performs a one-time migration from the pre-split combined config file
// (LEGACY_CONFIG_PATH) to the split config.json + state.json files. If the new files already exist,
// or the legacy file does not exist, it is a no-op. After a successful migration, the legacy file is
// renamed to LEGACY_CONFIG_PATH+".old" so it is not migrated again but is preserved as a backup.
func maybeMigrateLegacyConfig() error {
	configPath, err := configFilePath()
	if err != nil {
		return err
	}
	statePath, err := stateFilePath()
	if err != nil {
		return err
	}
	legacyPath, err := legacyConfigFilePath()
	if err != nil {
		return err
	}

	// If either new file already exists, we've already migrated (or started fresh); do nothing.
	_, configErr := os.Stat(configPath)
	_, stateErr := os.Stat(statePath)
	if configErr == nil || stateErr == nil {
		return nil
	}

	// Only migrate if the legacy file exists.
	legacyContents, err := os.ReadFile(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read legacy config file for migration: %w", err)
	}

	// The legacy file is a single flat JSON object mixing config and state fields. Unmarshal it into
	// both the config fields and the embedded ClientState (the latter is json:"-" on ClientConfig, so
	// it must be populated explicitly).
	var config ClientConfig
	if err := json.Unmarshal(legacyContents, &config); err != nil {
		return fmt.Errorf("failed to parse legacy config file for migration: %w", err)
	}
	if err := json.Unmarshal(legacyContents, &config.ClientState); err != nil {
		return fmt.Errorf("failed to parse legacy state fields for migration: %w", err)
	}

	// Write the split files, then rename the legacy file so we don't migrate again.
	if err := writeConfigAndState(&config); err != nil {
		return fmt.Errorf("failed to write split config/state during migration: %w", err)
	}
	if err := os.Rename(legacyPath, legacyPath+".old"); err != nil {
		return fmt.Errorf("failed to rename legacy config file after migration: %w", err)
	}
	return nil
}

func GetDefaultColorScheme() ColorScheme {
	return ColorScheme{
		SelectedBackground: "#3300ff",
		SelectedText:       "#ffff99",
		BorderColor:        "#585858",
	}
}

func GetConfig() (ClientConfig, error) {
	// Migrate a pre-split combined config into config.json + state.json before reading, so that
	// GetConfigContents below sees the split config file.
	if err := maybeMigrateLegacyConfig(); err != nil {
		return ClientConfig{}, err
	}
	configContents, err := GetConfigContents()
	if err != nil {
		return ClientConfig{}, err
	}
	var config ClientConfig
	// The embedded ClientState is tagged json:"-", so this only populates the config fields.
	err = json.Unmarshal(configContents, &config)
	if err != nil {
		return ClientConfig{}, fmt.Errorf("failed to parse config file: %w", err)
	}
	// Load the per-installation/volatile state from its separate file. A missing state file is
	// tolerated (state stays zero-valued) so that a config.json committed to VCS and checked out on
	// a fresh machine still works until the state file is (re)created.
	statePath, err := stateFilePath()
	if err != nil {
		return ClientConfig{}, err
	}
	stateContents, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(stateContents, &config.ClientState); err != nil {
			return ClientConfig{}, fmt.Errorf("failed to parse state file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ClientConfig{}, fmt.Errorf("failed to read state file: %w", err)
	}
	config.KeyBindings = config.KeyBindings.WithDefaults()
	if len(config.DisplayedColumns) == 0 {
		config.DisplayedColumns = []string{"Hostname", "CWD", "Timestamp", "Runtime", "Exit Code", "Command"}
	}
	if config.TimestampFormat == "" {
		config.TimestampFormat = "Jan 2 2006 15:04:05 MST"
	}
	if config.ColorScheme.SelectedBackground == "" {
		config.ColorScheme.SelectedBackground = GetDefaultColorScheme().SelectedBackground
	}
	if config.ColorScheme.SelectedText == "" {
		config.ColorScheme.SelectedText = GetDefaultColorScheme().SelectedText
	}
	if config.ColorScheme.BorderColor == "" {
		config.ColorScheme.BorderColor = GetDefaultColorScheme().BorderColor
	}
	if config.AiCompletionEndpoint == "" {
		// Default to the appropriate endpoint based on available API keys
		if os.Getenv("ANTHROPIC_API_KEY") != "" && os.Getenv("OPENAI_API_KEY") == "" {
			config.AiCompletionEndpoint = "https://api.anthropic.com/v1/chat/completions"
		} else {
			config.AiCompletionEndpoint = "https://api.openai.com/v1/chat/completions"
		}
	}
	if config.LogLevel == logrus.Level(0) {
		config.LogLevel = logrus.InfoLevel
	}
	if len(config.DefaultSearchColumns) == 0 {
		config.DefaultSearchColumns = []string{"command", "hostname", "current_working_directory"}
	}
	return config, nil
}

func SetConfig(config *ClientConfig) error {
	return writeConfigAndState(config)
}

// writeConfigAndState persists a ClientConfig across the two split files. The config file is
// pretty-printed for readability/editability (it is meant to be VCS-friendly), while the state file
// is written compactly since it changes on nearly every command. Each file is written atomically via
// a staged temp file + rename.
func writeConfigAndState(config *ClientConfig) error {
	if err := MakeHishtoryDir(); err != nil {
		return err
	}

	// Config: pretty-printed. The embedded ClientState is tagged json:"-", so it is excluded here.
	serializedConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}
	configPath, err := configFilePath()
	if err != nil {
		return err
	}
	if err := atomicWriteFile(configPath, serializedConfig); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// State: compact.
	serializedState, err := json.Marshal(&config.ClientState)
	if err != nil {
		return fmt.Errorf("failed to serialize state: %w", err)
	}
	statePath, err := stateFilePath()
	if err != nil {
		return err
	}
	if err := atomicWriteFile(statePath, serializedState); err != nil {
		return fmt.Errorf("failed to write state: %w", err)
	}
	return nil
}

// atomicWriteFile writes data to path atomically by staging it in a temp file and renaming.
func atomicWriteFile(destPath string, data []byte) error {
	stagedPath := destPath + ".tmp-" + uuid.Must(uuid.NewRandom()).String()
	if err := os.WriteFile(stagedPath, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(stagedPath, destPath); err != nil {
		return fmt.Errorf("failed to replace %s with the updated version: %w", destPath, err)
	}
	return nil
}

func InitConfig() error {
	if err := maybeMigrateLegacyConfig(); err != nil {
		return err
	}
	configPath, err := configFilePath()
	if err != nil {
		return err
	}
	_, err = os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return SetConfig(&ClientConfig{})
	}
	return err
}
