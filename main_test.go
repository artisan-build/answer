package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestLoadLauncherConfigMapsCloudResourcesAndOwner(t *testing.T) {
	values := validEnvironment()
	values["DATABASE_URL"] = "mysql://answer:p%40ss@mysql.internal:3306/answer_db"
	values["REDIS_URL"] = "rediss://cache-user:cache%40pass@cache.internal:6385/7"
	values["AWS_BUCKET"] = "physical-bucket-123"
	values["AWS_ENDPOINT_URL"] = "https://account.r2.example.invalid"
	values["AWS_URL"] = "https://public.example.invalid/files///"
	values["AWS_REGION"] = "not-the-plugin-region"
	values["PORT"] = "8080"
	values["SCALPELS_OWNER_NAME"] = "Example Owner"
	values["SCALPELS_OWNER_EMAIL"] = "owner@example.invalid"
	values["ADMIN_PASSWORD"] = "must-not-be-read"

	got, err := loadLauncherConfig("/srv/app", mapLookup(values))
	if err != nil {
		t.Fatalf("loadLauncherConfig() error = %v", err)
	}

	if got.dataPath != filepath.Join("/srv/app", "data") {
		t.Fatalf("dataPath = %q", got.dataPath)
	}
	if got.installEnvironment["ADMIN_NAME"] != "Example Owner" {
		t.Fatalf("ADMIN_NAME = %q", got.installEnvironment["ADMIN_NAME"])
	}
	if got.installEnvironment["ADMIN_EMAIL"] != "owner@example.invalid" {
		t.Fatalf("ADMIN_EMAIL = %q", got.installEnvironment["ADMIN_EMAIL"])
	}
	if _, found := got.installEnvironment["ADMIN_PASSWORD"]; found {
		t.Fatal("launcher config read an owner password from the environment")
	}
	if got.installEnvironment["SITE_NAME"] != "Apache Answer" {
		t.Fatalf("SITE_NAME = %q", got.installEnvironment["SITE_NAME"])
	}

	wantRedis := redisPluginSettings{
		Endpoint: "rediss://cache.internal:6385/7",
		Username: "cache-user",
		Password: "cache@pass",
	}
	if !reflect.DeepEqual(got.pluginSettings.Redis, wantRedis) {
		t.Fatalf("Redis settings mismatch\ngot:  %#v\nwant: %#v", got.pluginSettings.Redis, wantRedis)
	}
	wantS3 := s3PluginSettings{
		Endpoint:        "https://account.r2.example.invalid",
		BucketName:      "physical-bucket-123",
		ObjectKeyPrefix: "answer/",
		AccessKeyID:     "fabricated-access",
		AccessKeySecret: "fabricated-secret",
		VisitURLPrefix:  "https://public.example.invalid/files/",
		Region:          "auto",
		DisableSSL:      false,
	}
	if !reflect.DeepEqual(got.pluginSettings.S3, wantS3) {
		t.Fatalf("S3 settings mismatch\ngot:  %#v\nwant: %#v", got.pluginSettings.S3, wantS3)
	}
	for _, extension := range got.pluginSettings.AttachmentExtensions {
		if strings.HasPrefix(extension, ".") {
			t.Fatalf("attachment extension %q is not bare", extension)
		}
	}
	encoded, err := json.Marshal(got.pluginSettings.S3)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("ACL_PUBLIC_READ")) || bytes.Contains(encoded, []byte("acl_public_read")) {
		t.Fatalf("S3 settings contain a public ACL field: %s", encoded)
	}

	database := got.answerConfig["data"].(map[string]any)["database"].(map[string]any)
	if database["connection"] != "answer:p@ss@tcp(mysql.internal:3306)/answer_db?parseTime=true" {
		t.Fatalf("database connection = %q", database["connection"])
	}
}

func TestLoadLauncherConfigDerivesAnswerSafeOwnerName(t *testing.T) {
	tests := []struct {
		name       string
		ownerName  string
		ownerEmail string
		want       string
	}{
		{name: "already valid", ownerName: "Example Owner", ownerEmail: "owner@example.invalid", want: "Example Owner"},
		{name: "accented", ownerName: "José Álvarez", ownerEmail: "owner@example.invalid", want: "Jos lvarez"},
		{name: "apostrophe and punctuation", ownerName: "Siobhán O'Brien, Jr.", ownerEmail: "owner@example.invalid", want: "Siobh n O Brien Jr"},
		{name: "one character uses email local part", ownerName: "E", ownerEmail: "owner.local@example.invalid", want: "owner.local"},
		{name: "all invalid uses generic fallback", ownerName: "李雷💥", ownerEmail: "☃@example.invalid", want: "Owner"},
		{name: "over thirty is clamped", ownerName: "Zhang Wei Scalpels Operations Lead", ownerEmail: "owner@example.invalid", want: "Zhang Wei Scalpels Operations"},
	}
	answerNamePattern := regexp.MustCompile(`^[\w.\- ]{2,30}$`)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validEnvironment()
			values["SCALPELS_OWNER_NAME"] = test.ownerName
			values["SCALPELS_OWNER_EMAIL"] = test.ownerEmail

			config, err := loadLauncherConfig("/srv/app", mapLookup(values))
			if err != nil {
				t.Fatalf("loadLauncherConfig() error = %v", err)
			}
			got := config.installEnvironment["ADMIN_NAME"]
			if got != test.want {
				t.Fatalf("ADMIN_NAME = %q, want %q", got, test.want)
			}
			if !answerNamePattern.MatchString(got) {
				t.Fatalf("ADMIN_NAME %q does not match Answer's username rule", got)
			}
			if config.installEnvironment["ADMIN_EMAIL"] != test.ownerEmail {
				t.Fatalf("ADMIN_EMAIL = %q, want %q", config.installEnvironment["ADMIN_EMAIL"], test.ownerEmail)
			}
		})
	}
}

func TestRunConfiguresPluginsBeforeServerExec(t *testing.T) {
	events := []string{}
	runner := &fakeRunner{events: &events}
	store := &fakeStore{events: &events}
	var execEnvironment []string
	deps := testDependencies(t, runner, store)
	deps.execAnswer = func(_ string, _ []string, environment []string) error {
		events = append(events, "exec")
		execEnvironment = environment
		return nil
	}

	if err := run(context.Background(), nil, deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if want := []string{"cli:init", "store:configure", "exec"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if store.settings.Redis.Endpoint != "rediss://cache.internal:6385/7" {
		t.Fatalf("configured Redis endpoint = %q", store.settings.Redis.Endpoint)
	}
	if environmentValue(execEnvironment, "ADMIN_PASSWORD") != "" {
		t.Fatal("server exec environment retained the bootstrap password")
	}
	if environmentValue(execEnvironment, "ACL_PUBLIC_READ") != "" {
		t.Fatal("server exec environment retained ACL_PUBLIC_READ")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d", len(runner.calls))
	}
	if environmentValue(runner.calls[0].environment, "ADMIN_PASSWORD") != "generated-bootstrap-password" {
		t.Fatal("init did not receive the process-generated bootstrap password")
	}
	if strings.Contains(strings.Join(runner.calls[0].args, " "), "generated-bootstrap-password") {
		t.Fatal("bootstrap password reached Answer argv")
	}
	if environmentValue(runner.calls[0].environment, "ACL_PUBLIC_READ") != "" {
		t.Fatal("Answer init environment retained ACL_PUBLIC_READ")
	}
}

func TestPluginDatabaseValuesConfigureAndEnableBothPlugins(t *testing.T) {
	config, err := loadLauncherConfig("/srv/app", mapLookup(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	values, err := encodePluginValues(config.pluginSettings)
	if err != nil {
		t.Fatal(err)
	}

	var redisValue redisPluginSettings
	if err := json.Unmarshal([]byte(values["redis_cache"]), &redisValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(redisValue, config.pluginSettings.Redis) {
		t.Fatalf("stored Redis value = %#v", redisValue)
	}
	var s3Value map[string]any
	if err := json.Unmarshal([]byte(values["s3_storage"]), &s3Value); err != nil {
		t.Fatal(err)
	}
	if s3Value["bucket_name"] != "physical-bucket-123" || s3Value["region"] != "auto" || s3Value["object_key_prefix"] != "answer/" {
		t.Fatalf("stored S3 value = %#v", s3Value)
	}
	if _, found := s3Value["acl_public_read"]; found {
		t.Fatal("stored S3 value contains acl_public_read")
	}

	statusJSON, err := enablePluginStatus(`{"another_plugin":false}`)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]bool{}
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatal(err)
	}
	if !status["s3_storage"] || !status["redis_cache"] || status["another_plugin"] {
		t.Fatalf("stored plugin status = %#v", status)
	}

	advancedJSON, err := setAttachmentExtensions(`{"max_image_size":4}`, config.pluginSettings.AttachmentExtensions)
	if err != nil {
		t.Fatal(err)
	}
	var advanced struct {
		MaxImageSize         int      `json:"max_image_size"`
		AttachmentExtensions []string `json:"authorized_attachment_extensions"`
	}
	if err := json.Unmarshal([]byte(advancedJSON), &advanced); err != nil {
		t.Fatal(err)
	}
	if advanced.MaxImageSize != 4 || !reflect.DeepEqual(advanced.AttachmentExtensions, []string{"txt", "pdf", "zip"}) {
		t.Fatalf("stored advanced settings = %#v", advanced)
	}
}

func TestPrepareUpgradesThenConfiguresPlugins(t *testing.T) {
	events := []string{}
	runner := &fakeRunner{events: &events}
	store := &fakeStore{events: &events}
	deps := testDependencies(t, runner, store)
	deps.execAnswer = func(string, []string, []string) error {
		t.Fatal("prepare executed the server")
		return nil
	}

	if err := run(context.Background(), []string{"prepare"}, deps); err != nil {
		t.Fatalf("run(prepare) error = %v", err)
	}
	if want := []string{"cli:init", "cli:upgrade", "store:configure"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestRunRejectsKnownCLIErrorTextOnZeroExit(t *testing.T) {
	for _, known := range knownCLIErrorText {
		t.Run(known, func(t *testing.T) {
			events := []string{}
			runner := &fakeRunner{events: &events, output: []byte("prefix " + known + " detail")}
			store := &fakeStore{events: &events}
			deps := testDependencies(t, runner, store)

			err := run(context.Background(), nil, deps)
			if err == nil || !strings.Contains(err.Error(), known) {
				t.Fatalf("run() error = %v, want known CLI failure", err)
			}
			if want := []string{"cli:init"}; !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
		})
	}
}

func TestOwnerPasswordCommandRotatesAndEmitsOnlyNewPassword(t *testing.T) {
	oldHash, err := hashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{passwordHash: oldHash}
	runner := &fakeRunner{}
	passwords := []string{"first-new-password", "second-new-password"}
	nextPassword := 0
	deps := testDependencies(t, runner, store)
	deps.generatePassword = func() (string, error) {
		password := passwords[nextPassword]
		nextPassword++
		return password, nil
	}

	for index, password := range passwords {
		var output bytes.Buffer
		deps.stdout = &output
		if err := run(context.Background(), []string{ownerPasswordCommand}, deps); err != nil {
			t.Fatalf("rotation %d error = %v", index+1, err)
		}
		if got, want := output.String(), passwordOutputKey+"="+password+"\n"; got != want {
			t.Fatalf("rotation %d output = %q, want %q", index+1, got, want)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(store.passwordHash), []byte(password)); err != nil {
			t.Fatalf("rotation %d did not store the new credential: %v", index+1, err)
		}
		if index > 0 {
			if err := bcrypt.CompareHashAndPassword([]byte(store.passwordHash), []byte(passwords[index-1])); err == nil {
				t.Fatalf("rotation %d left the old password valid", index+1)
			}
		}
	}
	if err := bcrypt.CompareHashAndPassword([]byte(store.passwordHash), []byte("old-password")); err == nil {
		t.Fatal("original password remains valid")
	}
	if store.ownerEmail != "owner@example.invalid" {
		t.Fatalf("rotated owner email = %q", store.ownerEmail)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("owner password command invoked Answer CLI %d times", len(runner.calls))
	}
}

func TestOwnerPasswordCommandAcceptsNoPlaintextArgument(t *testing.T) {
	store := &fakeStore{}
	deps := testDependencies(t, &fakeRunner{}, store)
	generated := false
	deps.generatePassword = func() (string, error) {
		generated = true
		return "should-not-be-used", nil
	}

	err := run(context.Background(), []string{ownerPasswordCommand, "plaintext"}, deps)
	if err == nil || err.Error() != "usage: app owner-password" {
		t.Fatalf("run() error = %v", err)
	}
	if generated {
		t.Fatal("invalid command generated a password")
	}
	if store.passwordHash != "" {
		t.Fatal("invalid command changed the stored credential")
	}
}

func TestGeneratePasswordMeetsAnswerBounds(t *testing.T) {
	password, err := generatePassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 8 || len(password) > 32 {
		t.Fatalf("password length = %d", len(password))
	}
	if strings.ContainsAny(password, " \t\r\n") {
		t.Fatal("password contains whitespace")
	}
}

type runnerCall struct {
	args        []string
	environment []string
}

type fakeRunner struct {
	events *([]string)
	output []byte
	err    error
	calls  []runnerCall
}

func (runner *fakeRunner) Run(_ context.Context, _ string, args []string, environment []string) ([]byte, error) {
	runner.calls = append(runner.calls, runnerCall{args: append([]string(nil), args...), environment: append([]string(nil), environment...)})
	if runner.events != nil {
		*runner.events = append(*runner.events, "cli:"+args[0])
	}
	return runner.output, runner.err
}

type fakeStore struct {
	events       *([]string)
	settings     pluginSettings
	ownerEmail   string
	passwordHash string
	configureErr error
	passwordErr  error
}

func (store *fakeStore) Configure(_ context.Context, settings pluginSettings) error {
	if store.events != nil {
		*store.events = append(*store.events, "store:configure")
	}
	store.settings = settings
	return store.configureErr
}

func (store *fakeStore) SetOwnerPassword(_ context.Context, email, passwordHash string) error {
	if store.passwordErr != nil {
		return store.passwordErr
	}
	store.ownerEmail = email
	store.passwordHash = passwordHash
	return nil
}

func (store *fakeStore) Close() error { return nil }

func testDependencies(t *testing.T, runner commandRunner, store answerStore) dependencies {
	t.Helper()
	values := validEnvironment()
	baseEnvironment := make([]string, 0, len(values)+2)
	for key, value := range values {
		baseEnvironment = append(baseEnvironment, key+"="+value)
	}
	baseEnvironment = append(baseEnvironment, "ADMIN_PASSWORD=canary9", "ACL_PUBLIC_READ=true")
	return dependencies{
		root:            t.TempDir(),
		lookupEnv:       mapLookup(values),
		baseEnvironment: baseEnvironment,
		runner:          runner,
		storeFactory: func(string) (answerStore, error) {
			if store == nil {
				return nil, errors.New("store unavailable")
			}
			return store, nil
		},
		generatePassword: func() (string, error) {
			return "generated-bootstrap-password", nil
		},
		hashPassword:       hashPassword,
		execAnswer:         func(string, []string, []string) error { return nil },
		stdout:             &bytes.Buffer{},
		commandTimeout:     time.Second,
		configurationLimit: time.Second,
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"DATABASE_URL":          "mysql://answer:database-password@mysql.internal:3306/answer",
		"REDIS_URL":             "rediss://cache-user:cache-password@cache.internal:6385/7",
		"AWS_BUCKET":            "physical-bucket-123",
		"AWS_ENDPOINT_URL":      "https://account.r2.example.invalid",
		"AWS_ACCESS_KEY_ID":     "fabricated-access",
		"AWS_SECRET_ACCESS_KEY": "fabricated-secret",
		"AWS_URL":               "https://public.example.invalid/files/",
		"APP_URL":               "https://answer.example.invalid",
		"PORT":                  "8080",
		"SCALPELS_OWNER_NAME":   "Example Owner",
		"SCALPELS_OWNER_EMAIL":  "owner@example.invalid",
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, found := values[key]
		return value, found
	}
}

func environmentValue(environment []string, key string) string {
	for _, entry := range environment {
		entryKey, value, found := strings.Cut(entry, "=")
		if found && entryKey == key {
			return value
		}
	}
	return ""
}
