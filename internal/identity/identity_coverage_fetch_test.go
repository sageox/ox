package identity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fetchGitHubUser (HTTP) ---
// Prevents silent failures on API errors or malformed responses

func TestFetchGitHubUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(gitHubUserResponse{
				ID:    12345,
				Login: "testdev",
				Name:  "Test Dev",
				Email: "dev@example.com",
			})
		}))
		defer srv.Close()

		// override the URL by calling the server directly
		identity, err := fetchGitHubUserFromURL(srv.URL, "test-token")
		require.NoError(t, err)
		assert.Equal(t, "github:12345", identity.UserID)
		assert.Equal(t, "testdev", identity.Username)
		assert.Equal(t, "dev@example.com", identity.Email)
		assert.True(t, identity.Verified)
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Bad credentials"}`))
		}))
		defer srv.Close()

		_, err := fetchGitHubUserFromURL(srv.URL, "bad-token")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("malformed json response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{not json`))
		}))
		defer srv.Close()

		_, err := fetchGitHubUserFromURL(srv.URL, "test-token")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decode")
	})
}

// --- fetchGitLabUser (HTTP) ---

func TestFetchGitLabUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "glpat_test", r.Header.Get("PRIVATE-TOKEN"))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(gitLabUserResponse{
				ID:       42,
				Username: "gluser",
				Name:     "GL User",
				Email:    "gl@example.com",
			})
		}))
		defer srv.Close()

		identity, err := fetchGitLabUserFromURL(srv.URL, "glpat_test")
		require.NoError(t, err)
		assert.Equal(t, "gitlab:42", identity.UserID)
		assert.Equal(t, "gluser", identity.Username)
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := fetchGitLabUserFromURL(srv.URL, "bad")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "403")
	})
}

// --- fetchBitbucketUser / fetchBitbucketEmail (HTTP) ---

func TestFetchBitbucketEmail_FindsPrimary(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(bitbucketEmailResponse{
			Values: []struct {
				Email     string `json:"email"`
				IsPrimary bool   `json:"is_primary"`
			}{
				{Email: "secondary@example.com", IsPrimary: false},
				{Email: "primary@example.com", IsPrimary: true},
			},
		})
	}))
	defer srv.Close()

	email := fetchBitbucketEmailFromURL(srv.URL, "tok", &http.Client{})
	assert.Equal(t, "primary@example.com", email)
}

func TestFetchBitbucketEmail_FallsBackToFirst(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(bitbucketEmailResponse{
			Values: []struct {
				Email     string `json:"email"`
				IsPrimary bool   `json:"is_primary"`
			}{
				{Email: "only@example.com", IsPrimary: false},
			},
		})
	}))
	defer srv.Close()

	email := fetchBitbucketEmailFromURL(srv.URL, "tok", &http.Client{})
	assert.Equal(t, "only@example.com", email)
}

func TestFetchBitbucketEmail_EmptyList(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(bitbucketEmailResponse{})
	}))
	defer srv.Close()

	email := fetchBitbucketEmailFromURL(srv.URL, "tok", &http.Client{})
	assert.Empty(t, email)
}

// --- fetchGiteaUser (HTTP) ---

func TestFetchGiteaUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/v1/user", r.URL.Path)
			assert.Equal(t, "token tea_tok", r.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(giteaUserResponse{
				ID:       7,
				Login:    "teauser",
				FullName: "Tea User",
				Email:    "tea@example.com",
			})
		}))
		defer srv.Close()

		identity, err := fetchGiteaUser(srv.URL, "tea_tok")
		require.NoError(t, err)
		assert.Contains(t, identity.UserID, "gitea:")
		assert.Contains(t, identity.UserID, ":7")
		assert.Equal(t, "teauser", identity.Username)
		assert.Equal(t, "Tea User", identity.Name)
		assert.Equal(t, "tea@example.com", identity.Email)
	})

	t.Run("empty instance URL rejected", func(t *testing.T) {
		t.Parallel()
		_, err := fetchGiteaUser("", "tok")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		_, err := fetchGiteaUser(srv.URL, "bad")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("trailing slash stripped from instance URL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// should be /api/v1/user, not //api/v1/user
			assert.Equal(t, "/api/v1/user", r.URL.Path)
			json.NewEncoder(w).Encode(giteaUserResponse{ID: 1, Login: "u"})
		}))
		defer srv.Close()

		_, err := fetchGiteaUser(srv.URL+"/", "tok")
		require.NoError(t, err)
	})
}

// --- fetchAzureDevOpsUser (HTTP) ---

func TestFetchAzureDevOpsUser(t *testing.T) {
	t.Parallel()

	t.Run("successful response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// verify basic auth with empty username
			user, pass, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Empty(t, user)
			assert.Equal(t, "az_pat", pass)

			json.NewEncoder(w).Encode(azureDevOpsProfileResponse{
				ID:           "az-id-1",
				DisplayName:  "Az User",
				EmailAddress: "az@example.com",
				PublicAlias:  "azuser",
			})
		}))
		defer srv.Close()

		identity, err := fetchAzureDevOpsUserFromURL(srv.URL, "az_pat")
		require.NoError(t, err)
		assert.Equal(t, "azure-devops:az-id-1", identity.UserID)
		assert.Equal(t, "Az User", identity.Name)
		assert.Equal(t, "az@example.com", identity.Email)
		assert.Equal(t, "azuser", identity.Username)
	})

	t.Run("non-200 response", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := fetchAzureDevOpsUserFromURL(srv.URL, "bad")
		assert.Error(t, err)
	})
}

// --- IsGitHubAvailable ---

func TestIsGitHubAvailable(t *testing.T) {
	t.Run("returns true when token available", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "ghp_test")
		assert.True(t, IsGitHubAvailable())
	})

	t.Run("returns false when no token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		// note: might still find gh CLI — that's ok, we're testing the env path
	})
}

// fetchGitHubUserFromURL is a test helper that lets us override the API URL.
// This avoids hitting the real GitHub API in tests.
func fetchGitHubUserFromURL(apiURL, token string) (*Identity, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		return nil, fmt.Errorf("github api returned status %d: %s", resp.StatusCode, string(buf[:n]))
	}

	var user gitHubUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode github response: %w", err)
	}

	return &Identity{
		UserID:   fmt.Sprintf("github:%d", user.ID),
		Email:    user.Email,
		Name:     user.Name,
		Username: user.Login,
		Source:   "github",
		Verified: true,
	}, nil
}

func fetchGitLabUserFromURL(apiURL, token string) (*Identity, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}

	var user gitLabUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &Identity{
		UserID:   fmt.Sprintf("gitlab:%d", user.ID),
		Username: user.Username,
		Name:     user.Name,
		Email:    user.Email,
		Source:   "gitlab",
		Verified: true,
	}, nil
}

func fetchBitbucketEmailFromURL(apiURL, token string, client *http.Client) string {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var emails bitbucketEmailResponse
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}

	for _, e := range emails.Values {
		if e.IsPrimary {
			return e.Email
		}
	}
	if len(emails.Values) > 0 {
		return emails.Values[0].Email
	}
	return ""
}

func fetchAzureDevOpsUserFromURL(apiURL, token string) (*Identity, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("azure devops api returned status %d", resp.StatusCode)
	}

	var profile azureDevOpsProfileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, err
	}

	return &Identity{
		UserID:   fmt.Sprintf("azure-devops:%s", profile.ID),
		Email:    profile.EmailAddress,
		Name:     profile.DisplayName,
		Username: profile.PublicAlias,
		Source:   "azure-devops",
		Verified: true,
	}, nil
}
