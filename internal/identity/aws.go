package identity

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
	"gopkg.in/ini.v1"
)

// getAWSIdentity fetches the authenticated AWS user identity using STS GetCallerIdentity.
// It tries in order:
//  1. AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables
//  2. ~/.aws/credentials file (default profile or AWS_PROFILE)
//
// Note: This currently returns a limited identity based on local credentials.
// Full verification via STS GetCallerIdentity requires AWS Signature v4 signing,
// which would need the AWS SDK. For now, we extract what we can from local config.
//
// Privacy: Only called if a remote points to AWS CodeCommit URLs.
func getAWSIdentity() (*Identity, error) {
	accessKey, secretKey := getAWSCredentials()
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("no AWS credentials found")
	}

	// Try to get identity from AWS STS
	identity, err := fetchAWSCallerIdentity(accessKey, secretKey)
	if err != nil {
		// fallback: return limited identity based on access key
		// access key format: AKIA... (IAM user) or ASIA... (temporary credentials)
		return &Identity{
			UserID:   fmt.Sprintf("aws:%s", accessKey[:12]), // partial key for identification
			Source:   "aws",
			Verified: false,
		}, nil
	}

	return identity, nil
}

// getAWSCredentials retrieves AWS credentials from environment or ~/.aws/credentials.
func getAWSCredentials() (accessKey, secretKey string) {
	// 1. Environment variables (CI/CD friendly)
	accessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey != "" && secretKey != "" {
		return accessKey, secretKey
	}

	// 2. Read ~/.aws/credentials file
	return readAWSCredentials()
}

// readAWSCredentials reads AWS credentials from ~/.aws/credentials file.
// It respects AWS_PROFILE environment variable, otherwise uses [default] profile.
func readAWSCredentials() (accessKey, secretKey string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}

	credPath := filepath.Join(home, ".aws", "credentials")
	cfg, err := ini.Load(credPath)
	if err != nil {
		return "", ""
	}

	profile := os.Getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}

	section, err := cfg.GetSection(profile)
	if err != nil {
		return "", ""
	}

	accessKeyKey := section.Key("aws_access_key_id")
	secretKeyKey := section.Key("aws_secret_access_key")

	if accessKeyKey != nil && secretKeyKey != nil {
		return accessKeyKey.String(), secretKeyKey.String()
	}

	return "", ""
}

// stsCallerIdentityResponse represents the AWS STS GetCallerIdentity XML response.
type stsCallerIdentityResponse struct {
	XMLName xml.Name `xml:"GetCallerIdentityResponse"`
	Result  struct {
		Arn     string `xml:"Arn"`
		UserId  string `xml:"UserId"`
		Account string `xml:"Account"`
	} `xml:"GetCallerIdentityResult"`
}

// fetchAWSCallerIdentity calls AWS STS GetCallerIdentity to get identity info.
// Note: This is a simplified version that doesn't implement full AWS Signature v4.
// In production, use the AWS SDK for proper request signing.
func fetchAWSCallerIdentity(accessKey, secretKey string) (*Identity, error) {
	// AWS STS GetCallerIdentity endpoint
	endpoint := "https://sts.amazonaws.com/"

	// build query parameters
	params := url.Values{}
	params.Set("Action", "GetCallerIdentity")
	params.Set("Version", "2011-06-15")

	// create request
	// Note: AWS requires Signature v4 signing for authentication
	// This simplified version won't work without proper signing
	reqURL := endpoint + "?" + params.Encode()
	req, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", useragent.String())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// TODO: Implement AWS Signature v4 signing
	// For now, this will fail authentication, so we return an error
	// that triggers the fallback in getAWSIdentity

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return nil, fmt.Errorf("aws sts request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("aws sts returned status %d: %s", resp.StatusCode, string(body))
	}

	var result stsCallerIdentityResponse
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode aws sts response: %w", err)
	}

	// parse ARN to extract account and user/role name
	account, name := parseAWSArn(result.Result.Arn)

	return &Identity{
		UserID:   fmt.Sprintf("aws:%s:%s", account, name),
		Name:     name,
		Source:   "aws",
		Verified: true,
	}, nil
}

// parseAWSArn extracts account ID and user/role name from an AWS ARN.
// Examples:
//   - arn:aws:iam::123456789012:user/alice -> (123456789012, alice)
//   - arn:aws:sts::123456789012:assumed-role/dev-role/session -> (123456789012, dev-role)
func parseAWSArn(arn string) (account, name string) {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return "", ""
	}

	account = parts[4]

	// resource part is after the 5th colon
	// format: user/username or assumed-role/role-name/session-name
	resource := parts[5]
	resourceParts := strings.Split(resource, "/")

	if len(resourceParts) >= 2 {
		// for assumed-role, use the role name (second part)
		// for user, use the user name (second part)
		name = resourceParts[1]
	}

	return account, name
}
