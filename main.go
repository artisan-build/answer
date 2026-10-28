package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

const (
	ownerPasswordCommand = "owner-password"
	passwordOutputKey    = "SCALPELS_OWNER_PASSWORD"
)

var knownCLIErrorText = []string{
	"migrate failed:",
	"try to init by env fail:",
	"read config failed:",
	"set default config failed:",
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot determine the application directory")
		os.Exit(1)
	}

	deps := dependencies{
		root:               root,
		lookupEnv:          os.LookupEnv,
		baseEnvironment:    os.Environ(),
		runner:             execCommandRunner{},
		storeFactory:       newMySQLStore,
		generatePassword:   generatePassword,
		hashPassword:       hashPassword,
		execAnswer:         execAnswer,
		stdout:             os.Stdout,
		commandTimeout:     2 * time.Minute,
		configurationLimit: 30 * time.Second,
	}
	if err := run(context.Background(), os.Args[1:], deps); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type launcherConfig struct {
	dataPath           string
	databaseDSN        string
	answerConfig       map[string]any
	installEnvironment map[string]string
	pluginSettings     pluginSettings
	ownerEmail         string
}

type redisPluginSettings struct {
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type s3PluginSettings struct {
	Endpoint        string `json:"endpoint"`
	BucketName      string `json:"bucket_name"`
	ObjectKeyPrefix string `json:"object_key_prefix"`
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
	AccessToken     string `json:"access_token"`
	VisitURLPrefix  string `json:"visit_url_prefix"`
	Region          string `json:"region"`
	DisableSSL      bool   `json:"disable_ssl"`
}

type pluginSettings struct {
	Redis                redisPluginSettings
	S3                   s3PluginSettings
	AttachmentExtensions []string
}

func loadLauncherConfig(root string, lookupEnv func(string) (string, bool)) (launcherConfig, error) {
	databaseURL, err := requiredEnvironment(lookupEnv, "DATABASE_URL")
	if err != nil {
		return launcherConfig{}, err
	}
	database, err := url.Parse(databaseURL)
	if err != nil || database.Scheme != "mysql" || database.User == nil || database.Host == "" || strings.TrimPrefix(database.Path, "/") == "" {
		return launcherConfig{}, errors.New("DATABASE_URL must be a MySQL connection URL")
	}
	db := mysql.NewConfig()
	db.User = database.User.Username()
	db.Passwd, _ = database.User.Password()
	db.Net, db.Addr, db.DBName = "tcp", database.Host, strings.TrimPrefix(database.Path, "/")
	db.ParseTime = true

	ownerName, err := requiredEnvironment(lookupEnv, "SCALPELS_OWNER_NAME")
	if err != nil {
		return launcherConfig{}, err
	}
	ownerEmail, err := requiredEnvironment(lookupEnv, "SCALPELS_OWNER_EMAIL")
	if err != nil {
		return launcherConfig{}, err
	}
	ownerName = answerOwnerName(ownerName, ownerEmail)
	appURL, err := requiredEnvironment(lookupEnv, "APP_URL")
	if err != nil {
		return launcherConfig{}, err
	}

	redisSettings, err := loadRedisSettings(lookupEnv)
	if err != nil {
		return launcherConfig{}, err
	}
	s3Settings, err := loadS3Settings(lookupEnv)
	if err != nil {
		return launcherConfig{}, err
	}

	port, _ := lookupEnv("PORT")
	if port == "" {
		port = "3000"
	}
	data := filepath.Join(root, "data")

	return launcherConfig{
		dataPath:    data,
		databaseDSN: db.FormatDSN(),
		answerConfig: map[string]any{
			"server": map[string]any{"http": map[string]any{"addr": "[::]:" + port}},
			"data": map[string]any{
				"database": map[string]any{"driver": "mysql", "connection": db.FormatDSN()},
				"cache":    map[string]any{"file_path": filepath.Join(data, "cache/cache.db")},
			},
			"i18n":           map[string]any{"bundle_dir": filepath.Join(data, "i18n")},
			"swaggerui":      map[string]any{"show": false},
			"service_config": map[string]any{"upload_path": filepath.Join(data, "uploads"), "clean_up_uploads": true, "clean_orphan_uploads_period_hours": 48, "purge_deleted_files_period_days": 30},
			"ui":             map[string]any{"base_url": "", "api_base_url": ""},
		},
		installEnvironment: map[string]string{
			"AUTO_INSTALL": "true", "DB_TYPE": "mysql", "DB_HOST": db.Addr,
			"DB_USERNAME": db.User, "DB_PASSWORD": db.Passwd, "DB_NAME": db.DBName,
			"LANGUAGE": "en_US", "SITE_NAME": "Apache Answer", "SITE_URL": appURL,
			"CONTACT_EMAIL": ownerEmail, "ADMIN_NAME": ownerName, "ADMIN_EMAIL": ownerEmail,
			"EXTERNAL_CONTENT_DISPLAY": "ask_before_display", "INSTALL_PORT": "19080",
			"SITE_ADDR": "[::]:" + port,
		},
		pluginSettings: pluginSettings{
			Redis:                redisSettings,
			S3:                   s3Settings,
			AttachmentExtensions: []string{"txt", "pdf", "zip"},
		},
		ownerEmail: ownerEmail,
	}, nil
}

func answerOwnerName(name, email string) string {
	if sanitized := sanitizeAnswerOwnerName(name); len(sanitized) >= 2 {
		return sanitized
	}

	localPart, _, _ := strings.Cut(email, "@")
	if sanitized := sanitizeAnswerOwnerName(localPart); len(sanitized) >= 2 {
		return sanitized
	}

	return "Owner"
}

func sanitizeAnswerOwnerName(value string) string {
	const maxLength = 30
	const separators = " .-_"

	var sanitized strings.Builder
	lastWasSeparator := true
	for _, character := range value {
		isLetter := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		isSeparator := character < 128 && strings.ContainsRune(separators, character)
		if !isLetter && !isDigit && !isSeparator {
			character = ' '
			isSeparator = true
		}
		if isSeparator {
			if lastWasSeparator {
				continue
			}
			lastWasSeparator = true
		} else {
			lastWasSeparator = false
		}
		sanitized.WriteRune(character)
	}

	result := sanitized.String()
	if len(result) > maxLength {
		result = result[:maxLength]
	}
	return strings.TrimRight(result, separators)
}

func loadRedisSettings(lookupEnv func(string) (string, bool)) (redisPluginSettings, error) {
	raw, err := requiredEnvironment(lookupEnv, "REDIS_URL")
	if err != nil {
		return redisPluginSettings{}, err
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") || parsed.Hostname() == "" {
		return redisPluginSettings{}, errors.New("REDIS_URL must be a redis:// or rediss:// connection URL")
	}

	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	parsed.User = nil

	return redisPluginSettings{
		Endpoint: parsed.String(),
		Username: username,
		Password: password,
	}, nil
}

func loadS3Settings(lookupEnv func(string) (string, bool)) (s3PluginSettings, error) {
	endpoint, _ := lookupEnv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		endpoint, _ = lookupEnv("AWS_ENDPOINT")
	}
	if endpoint == "" {
		return s3PluginSettings{}, errors.New("AWS_ENDPOINT_URL is required")
	}
	bucket, err := requiredEnvironment(lookupEnv, "AWS_BUCKET")
	if err != nil {
		return s3PluginSettings{}, err
	}
	accessKey, err := requiredEnvironment(lookupEnv, "AWS_ACCESS_KEY_ID")
	if err != nil {
		return s3PluginSettings{}, err
	}
	secretKey, err := requiredEnvironment(lookupEnv, "AWS_SECRET_ACCESS_KEY")
	if err != nil {
		return s3PluginSettings{}, err
	}
	visitURL, err := requiredEnvironment(lookupEnv, "AWS_URL")
	if err != nil {
		return s3PluginSettings{}, err
	}
	parsedVisitURL, parseErr := url.Parse(visitURL)
	if parseErr != nil || (parsedVisitURL.Scheme != "http" && parsedVisitURL.Scheme != "https") || parsedVisitURL.Host == "" {
		return s3PluginSettings{}, errors.New("AWS_URL must be a public HTTP or HTTPS URL")
	}
	token, _ := lookupEnv("AWS_SESSION_TOKEN")

	return s3PluginSettings{
		Endpoint:        endpoint,
		BucketName:      bucket,
		ObjectKeyPrefix: "answer/",
		AccessKeyID:     accessKey,
		AccessKeySecret: secretKey,
		AccessToken:     token,
		VisitURLPrefix:  strings.TrimRight(visitURL, "/") + "/",
		Region:          "auto",
		DisableSSL:      false,
	}, nil
}

func requiredEnvironment(lookupEnv func(string) (string, bool), key string) (string, error) {
	value, found := lookupEnv(key)
	if !found || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

type commandRunner interface {
	Run(ctx context.Context, executable string, args []string, environment []string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, executable string, args []string, environment []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = environment
	return cmd.CombinedOutput()
}

type answerStore interface {
	Configure(ctx context.Context, settings pluginSettings) error
	SetOwnerPassword(ctx context.Context, email, passwordHash string) error
	Close() error
}

type dependencies struct {
	root               string
	lookupEnv          func(string) (string, bool)
	baseEnvironment    []string
	runner             commandRunner
	storeFactory       func(string) (answerStore, error)
	generatePassword   func() (string, error)
	hashPassword       func(string) (string, error)
	execAnswer         func(string, []string, []string) error
	stdout             io.Writer
	commandTimeout     time.Duration
	configurationLimit time.Duration
}

func run(ctx context.Context, args []string, deps dependencies) error {
	config, err := loadLauncherConfig(deps.root, deps.lookupEnv)
	if err != nil {
		return err
	}

	if len(args) > 0 && args[0] == ownerPasswordCommand {
		if len(args) != 1 {
			return fmt.Errorf("usage: app %s", ownerPasswordCommand)
		}
		return rotateOwnerPassword(ctx, config, deps)
	}
	if len(args) > 1 || (len(args) == 1 && args[0] != "prepare") {
		return errors.New("usage: app [prepare|owner-password]")
	}

	if err := writeAnswerConfig(config); err != nil {
		return err
	}
	bootstrapPassword, err := deps.generatePassword()
	if err != nil {
		return errors.New("cannot generate the bootstrap password")
	}
	installOverrides := cloneMap(config.installEnvironment)
	installOverrides["ADMIN_PASSWORD"] = bootstrapPassword
	installEnvironment := mergeEnvironment(deps.baseEnvironment, installOverrides, "ACL_PUBLIC_READ", "ADMIN_PASSWORD")
	answer := filepath.Join(deps.root, "answer")

	if err := runAnswerCommand(ctx, deps, answer, "init", installEnvironment, config.dataPath); err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "prepare" {
		if err := runAnswerCommand(ctx, deps, answer, "upgrade", installEnvironment, config.dataPath); err != nil {
			return err
		}
	}

	store, err := deps.storeFactory(config.databaseDSN)
	if err != nil {
		return errors.New("cannot open the Answer database")
	}
	configureCtx, cancel := context.WithTimeout(ctx, deps.configurationLimit)
	err = store.Configure(configureCtx, config.pluginSettings)
	cancel()
	closeErr := store.Close()
	if err != nil {
		return errors.New("cannot configure Answer plugins")
	}
	if closeErr != nil {
		return errors.New("cannot close the Answer database")
	}

	if len(args) == 1 {
		return nil
	}
	runtimeEnvironment := mergeEnvironment(deps.baseEnvironment, nil, "ACL_PUBLIC_READ", "ADMIN_PASSWORD")
	return deps.execAnswer(answer, []string{answer, "run", "-C", config.dataPath}, runtimeEnvironment)
}

func runAnswerCommand(ctx context.Context, deps dependencies, answer, command string, environment []string, dataPath string) error {
	commandCtx, cancel := context.WithTimeout(ctx, deps.commandTimeout)
	defer cancel()
	output, runErr := deps.runner.Run(commandCtx, answer, []string{command, "-C", dataPath}, environment)
	if runErr != nil {
		return fmt.Errorf("answer %s failed", command)
	}
	for _, known := range knownCLIErrorText {
		if strings.Contains(string(output), known) {
			return fmt.Errorf("answer %s failed: %s", command, known)
		}
	}
	return nil
}

func writeAnswerConfig(config launcherConfig) error {
	for _, directory := range []string{"conf", "cache", "uploads", "i18n"} {
		if err := os.MkdirAll(filepath.Join(config.dataPath, directory), 0700); err != nil {
			return errors.New("cannot create the Answer data directory")
		}
	}
	encoded, err := json.MarshalIndent(config.answerConfig, "", "  ")
	if err != nil {
		return errors.New("cannot encode the Answer configuration")
	}
	if err := os.WriteFile(filepath.Join(config.dataPath, "conf/config.yaml"), encoded, 0600); err != nil {
		return errors.New("cannot write the Answer configuration")
	}
	return nil
}

func rotateOwnerPassword(ctx context.Context, config launcherConfig, deps dependencies) error {
	password, err := deps.generatePassword()
	if err != nil {
		return errors.New("cannot generate the owner password")
	}
	passwordHash, err := deps.hashPassword(password)
	if err != nil {
		return errors.New("cannot hash the owner password")
	}
	store, err := deps.storeFactory(config.databaseDSN)
	if err != nil {
		return errors.New("cannot open the Answer database")
	}
	rotationCtx, cancel := context.WithTimeout(ctx, deps.configurationLimit)
	err = store.SetOwnerPassword(rotationCtx, config.ownerEmail, passwordHash)
	cancel()
	closeErr := store.Close()
	if err != nil {
		return errors.New("cannot rotate the Answer owner password")
	}
	if closeErr != nil {
		return errors.New("cannot close the Answer database")
	}
	_, err = fmt.Fprintf(deps.stdout, "%s=%s\n", passwordOutputKey, password)
	return err
}

func generatePassword() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func cloneMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mergeEnvironment(base []string, overrides map[string]string, remove ...string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, found := strings.Cut(entry, "=")
		if found {
			merged[key] = value
		}
	}
	for _, key := range remove {
		delete(merged, key)
	}
	for key, value := range overrides {
		merged[key] = value
	}
	result := make([]string, 0, len(merged))
	for key, value := range merged {
		result = append(result, key+"="+value)
	}
	return result
}

func execAnswer(executable string, args, environment []string) error {
	return syscall.Exec(executable, args, environment)
}

type mysqlStore struct {
	db *sql.DB
}

func newMySQLStore(dsn string) (answerStore, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &mysqlStore{db: db}, nil
}

func (store *mysqlStore) Close() error {
	return store.db.Close()
}

func (store *mysqlStore) Configure(ctx context.Context, settings pluginSettings) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()

	pluginValues, err := encodePluginValues(settings)
	if err != nil {
		return err
	}
	for _, slug := range []string{"s3_storage", "redis_cache"} {
		value := pluginValues[slug]
		if _, err = transaction.ExecContext(ctx, `INSERT INTO plugin_config (plugin_slug_name, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = ?`, slug, value, value); err != nil {
			return err
		}
	}

	var statusValue string
	if err = transaction.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = 'plugin.status' FOR UPDATE").Scan(&statusValue); err != nil {
		return err
	}
	encodedStatus, err := enablePluginStatus(statusValue)
	if err != nil {
		return err
	}
	if _, err = transaction.ExecContext(ctx, "UPDATE config SET value = ? WHERE `key` = 'plugin.status'", encodedStatus); err != nil {
		return err
	}

	var advancedValue string
	if err = transaction.QueryRowContext(ctx, `SELECT content FROM site_info WHERE type = 'advanced' FOR UPDATE`).Scan(&advancedValue); err != nil {
		return err
	}
	encodedAdvanced, err := setAttachmentExtensions(advancedValue, settings.AttachmentExtensions)
	if err != nil {
		return err
	}
	if _, err = transaction.ExecContext(ctx, `UPDATE site_info SET content = ? WHERE type = 'advanced'`, encodedAdvanced); err != nil {
		return err
	}

	return transaction.Commit()
}

func encodePluginValues(settings pluginSettings) (map[string]string, error) {
	values := make(map[string]string, 2)
	for slug, config := range map[string]any{
		"s3_storage":  settings.S3,
		"redis_cache": settings.Redis,
	} {
		encoded, err := json.Marshal(config)
		if err != nil {
			return nil, err
		}
		values[slug] = string(encoded)
	}
	return values, nil
}

func enablePluginStatus(current string) (string, error) {
	status := make(map[string]bool)
	if err := json.Unmarshal([]byte(current), &status); err != nil {
		return "", err
	}
	status["s3_storage"] = true
	status["redis_cache"] = true
	encoded, err := json.Marshal(status)
	return string(encoded), err
}

func setAttachmentExtensions(current string, extensions []string) (string, error) {
	advanced := make(map[string]any)
	if err := json.Unmarshal([]byte(current), &advanced); err != nil {
		return "", err
	}
	advanced["authorized_attachment_extensions"] = extensions
	encoded, err := json.Marshal(advanced)
	return string(encoded), err
}

func (store *mysqlStore) SetOwnerPassword(ctx context.Context, email, passwordHash string) error {
	result, err := store.db.ExecContext(ctx, "UPDATE `user` AS target_user\n"+
		`SET pass = ?
		WHERE target_user.e_mail = ?
		AND EXISTS (
			SELECT 1
			FROM user_role_rel AS owner_role
			WHERE owner_role.user_id = target_user.id
			AND owner_role.role_id = 2
		)`, passwordHash, email)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("owner account not found")
	}
	return nil
}
