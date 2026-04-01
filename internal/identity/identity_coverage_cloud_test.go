package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- parseAWSArn ---
// Prevents mishandling of ARN formats (IAM user vs assumed role vs malformed)

func TestParseAWSArn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arn         string
		wantAccount string
		wantName    string
	}{
		{
			name:        "iam user",
			arn:         "arn:aws:iam::123456789012:user/alice",
			wantAccount: "123456789012",
			wantName:    "alice",
		},
		{
			name:        "assumed role",
			arn:         "arn:aws:sts::123456789012:assumed-role/dev-role/session-name",
			wantAccount: "123456789012",
			wantName:    "dev-role",
		},
		{
			name:        "root account (no slash in resource)",
			arn:         "arn:aws:iam::123456789012:root",
			wantAccount: "123456789012",
			wantName:    "",
		},
		{
			name:        "too few parts",
			arn:         "arn:aws:iam",
			wantAccount: "",
			wantName:    "",
		},
		{
			name:        "empty string",
			arn:         "",
			wantAccount: "",
			wantName:    "",
		},
		{
			name:        "exactly 6 parts with user path",
			arn:         "arn:aws:iam::999999999999:user/bob",
			wantAccount: "999999999999",
			wantName:    "bob",
		},
		{
			name:        "federated user with nested path",
			arn:         "arn:aws:sts::111111111111:federated-user/devops/extra",
			wantAccount: "111111111111",
			wantName:    "devops",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			account, name := parseAWSArn(tt.arn)
			assert.Equal(t, tt.wantAccount, account)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

// --- parseGCloudConfigAccount ---
// Prevents INI parsing regressions: section boundaries, missing values, whitespace

func TestParseGCloudConfigAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "standard config",
			content: `[core]
account = user@example.com
project = my-project
`,
			want: "user@example.com",
		},
		{
			name: "account in non-core section is ignored",
			content: `[compute]
account = wrong@example.com

[core]
account = right@example.com
`,
			want: "right@example.com",
		},
		{
			name: "no core section",
			content: `[compute]
region = us-central1
`,
			want: "",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name: "core section with no account key",
			content: `[core]
project = my-project
`,
			want: "",
		},
		{
			name: "account with extra whitespace",
			content: `[core]
account =   padded@example.com
`,
			want: "padded@example.com",
		},
		{
			name: "core section followed by another section resets scope",
			content: `[core]
project = p1

[other]
account = wrong@example.com
`,
			want: "",
		},
		{
			name: "no equals sign in account line",
			content: `[core]
account user@example.com
`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseGCloudConfigAccount(tt.content))
		})
	}
}

// --- readServiceAccountCredentials ---
// Prevents accepting non-service-account files or files missing required fields

func TestReadServiceAccountCredentials(t *testing.T) {
	t.Parallel()

	t.Run("valid service account", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{
			"type":         "service_account",
			"client_email": "sa@project.iam.gserviceaccount.com",
			"project_id":   "my-project",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		identity, err := readServiceAccountCredentials(path)
		require.NoError(t, err)
		assert.Equal(t, "gcp:sa@project.iam.gserviceaccount.com", identity.UserID)
		assert.Equal(t, "sa@project.iam.gserviceaccount.com", identity.Email)
		assert.Equal(t, "gcp", identity.Source)
		assert.False(t, identity.Verified)
	})

	t.Run("wrong type rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{
			"type":         "authorized_user",
			"client_email": "user@example.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		_, err := readServiceAccountCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not a service account")
	})

	t.Run("missing client_email rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{"type": "service_account"}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		_, err := readServiceAccountCredentials(path)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing client_email")
	})

	t.Run("file not found", func(t *testing.T) {
		t.Parallel()
		_, err := readServiceAccountCredentials("/nonexistent/path.json")
		assert.Error(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

		_, err := readServiceAccountCredentials(path)
		assert.Error(t, err)
	})
}

// --- readGCloudConfig (file-based) ---
// Prevents silent failures when gcloud config is missing or malformed

func TestReadGCloudConfig(t *testing.T) {
	t.Run("reads valid config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		configDir := filepath.Join(dir, "gcloud", "configurations")
		require.NoError(t, os.MkdirAll(configDir, 0o755))
		content := "[core]\naccount = dev@example.com\nproject = my-proj\n"
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config_default"), []byte(content), 0o644))

		identity := readGCloudConfig()
		require.NotNil(t, identity)
		assert.Equal(t, "gcp:dev@example.com", identity.UserID)
		assert.Equal(t, "dev@example.com", identity.Email)
		assert.False(t, identity.Verified)
	})

	t.Run("returns nil when config missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Nil(t, readGCloudConfig())
	})

	t.Run("returns nil when no account in config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		configDir := filepath.Join(dir, "gcloud", "configurations")
		require.NoError(t, os.MkdirAll(configDir, 0o755))
		content := "[compute]\nregion = us-west1\n"
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config_default"), []byte(content), 0o644))

		assert.Nil(t, readGCloudConfig())
	})
}

// --- readApplicationDefaultCredentials ---
// Prevents ignoring service account ADC or accepting authorized_user (which needs API)

func TestReadApplicationDefaultCredentials(t *testing.T) {
	t.Run("reads service account ADC", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		adcDir := filepath.Join(dir, "gcloud")
		require.NoError(t, os.MkdirAll(adcDir, 0o755))

		creds := map[string]string{
			"type":         "service_account",
			"client_email": "sa@proj.iam.gserviceaccount.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(filepath.Join(adcDir, "application_default_credentials.json"), data, 0o644))

		identity := readApplicationDefaultCredentials()
		require.NotNil(t, identity)
		assert.Equal(t, "sa@proj.iam.gserviceaccount.com", identity.Email)
	})

	t.Run("authorized_user returns nil (needs API)", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		adcDir := filepath.Join(dir, "gcloud")
		require.NoError(t, os.MkdirAll(adcDir, 0o755))

		creds := map[string]string{
			"type":      "authorized_user",
			"client_id": "1234.apps.googleusercontent.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(filepath.Join(adcDir, "application_default_credentials.json"), data, 0o644))

		assert.Nil(t, readApplicationDefaultCredentials())
	})

	t.Run("missing file returns nil", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Nil(t, readApplicationDefaultCredentials())
	})
}

// --- readAzureCliConfig ---
// Prevents token lookup failures when azure config is missing or malformed

func TestReadAzureCliConfig(t *testing.T) {
	t.Run("reads pat_token from config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)

		azureDir := filepath.Join(dir, ".azure")
		require.NoError(t, os.MkdirAll(azureDir, 0o755))

		content := `devops:
  pat_token: azure_pat_123
`
		require.NoError(t, os.WriteFile(filepath.Join(azureDir, "config"), []byte(content), 0o644))

		assert.Equal(t, "azure_pat_123", readAzureCliConfig())
	})

	t.Run("returns empty when config missing", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		assert.Empty(t, readAzureCliConfig())
	})
}

// --- GCP env var path ---

func TestGetGCPIdentity_ServiceAccountEnv(t *testing.T) {
	t.Run("reads from GOOGLE_APPLICATION_CREDENTIALS", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sa.json")

		creds := map[string]string{
			"type":         "service_account",
			"client_email": "sa@proj.iam.gserviceaccount.com",
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(path, data, 0o644))

		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // prevent other lookups

		identity, err := getGCPIdentity()
		require.NoError(t, err)
		assert.Equal(t, "sa@proj.iam.gserviceaccount.com", identity.Email)
	})

	t.Run("falls through on invalid credentials file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))

		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := getGCPIdentity()
		assert.Error(t, err) // no valid creds found anywhere
	})
}

// --- AWS env var path ---

func TestGetAWSCredentials_EnvVars(t *testing.T) {
	t.Run("reads from env vars", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")

		ak, sk := getAWSCredentials()
		assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", ak)
		assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", sk)
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		t.Setenv("HOME", t.TempDir()) // prevent reading real credentials

		ak, sk := getAWSCredentials()
		assert.Empty(t, ak)
		assert.Empty(t, sk)
	})
}

// --- parseGCloudProperties ---

func TestParseGCloudProperties(t *testing.T) {
	t.Run("reads properties file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		propsDir := filepath.Join(dir, "gcloud")
		require.NoError(t, os.MkdirAll(propsDir, 0o755))

		content := "[core]\naccount = props@example.com\n"
		require.NoError(t, os.WriteFile(filepath.Join(propsDir, "properties"), []byte(content), 0o644))

		identity := parseGCloudProperties()
		require.NotNil(t, identity)
		assert.Equal(t, "gcp:props@example.com", identity.UserID)
	})

	t.Run("returns nil when properties missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Nil(t, parseGCloudProperties())
	})
}

// --- getAWSIdentity ---
// Prevents panics or wrong identity format when STS call fails (common case)

func TestGetAWSIdentity_FallbackOnSTSFailure(t *testing.T) {
	// STS call will fail (no real signing), should return partial identity from access key
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")

	identity, err := getAWSIdentity()
	require.NoError(t, err)
	assert.Equal(t, "aws", identity.Source)
	assert.False(t, identity.Verified) // fallback is unverified
	assert.Contains(t, identity.UserID, "aws:AKIAIOSFODNN")
}

func TestGetAWSIdentity_NoCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("HOME", t.TempDir())

	_, err := getAWSIdentity()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no AWS credentials")
}

// --- readApplicationDefaultCredentials edge case ---
// Prevents crash on malformed JSON in ADC file

func TestReadApplicationDefaultCredentials_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	adcDir := filepath.Join(dir, "gcloud")
	require.NoError(t, os.MkdirAll(adcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(adcDir, "application_default_credentials.json"), []byte("{bad json"), 0o644))

	assert.Nil(t, readApplicationDefaultCredentials())
}
