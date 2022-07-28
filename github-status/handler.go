package function

import (
	"context"
	"fmt"
	"github.com/alexellis/derek/config"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/alexellis/derek/auth"
	"github.com/alexellis/derek/factory"
	"github.com/alexellis/hmac"
	"github.com/google/go-github/github"
	"github.com/openfaas/openfaas-cloud/sdk"
)

const (
	defaultPrivateKeyName    = "private-key"
	defaultPayloadSecretName = "payload-secret"
	defaultSecretMountPath   = "/var/openfaas/secrets"
	githubCheckCompleted     = "completed"
	githubCheckQueued        = "queued"
	githubConclusionFailure  = "failure"
	githubConclusionSuccess  = "success"
	githubConclusionNeutral  = "neutral"
)

var (
	token = ""
)

// Handle reports the building process of the
// function along with the function stack by sending
// commit statuses to GitHub on pending, failure or success
func Handle(req []byte) string {
	if sdk.HmacEnabled() {

		key, keyErr := sdk.ReadSecret("payload-secret")
		if keyErr != nil {
			fmt.Fprintf(os.Stderr, keyErr.Error())
			os.Exit(-1)
		}

		digest := os.Getenv("Http_X_Cloud_Signature")

		validated := hmac.Validate(req, digest, key)

		if validated != nil {
			fmt.Fprintf(os.Stderr, validated.Error())
			os.Exit(-1)
		}
		fmt.Fprintf(os.Stderr, "hash for HMAC validated successfully\n")
	}

	status, marshalErr := sdk.UnmarshalStatus(req)
	if marshalErr != nil {
		log.Fatal("failed to parse status request json, error: ", marshalErr.Error())
	}

	if len(status.CommitStatuses) == 0 {
		log.Fatal("failed commit statuses are empty: ", status.CommitStatuses)
	}

	// use auth token if provided
	if status.AuthToken != sdk.EmptyAuthToken && sdk.ValidToken(status.AuthToken) {
		token = status.AuthToken
		log.Printf("reusing provided auth token")
	} else {
		var tokenErr error
		privateKey, err := sdk.ReadSecret(defaultPrivateKeyName)
		if err != nil {
			fmt.Sprintf("Error reading privateKey: %v", err)
			return "Error reading github private Key"
		}
		token, tokenErr = auth.MakeAccessTokenForInstallation(os.Getenv("github_app_id"), status.EventInfo.InstallationID, privateKey)
		if tokenErr != nil {
			log.Fatalf("failed to report status %v, error: %s\n", status, tokenErr.Error())
		}

		if token == "" {
			log.Fatalf("failed to report status %v, error: authentication failed Invalid token\n", status)
		}

		log.Printf("auth token is created")
	}

	for _, commitStatus := range status.CommitStatuses {
		err := reportToGithub(&commitStatus, &status.EventInfo)
		if err != nil {
			log.Fatalf("failed to report status %v, error: %s", status, err.Error())
		}
	}

	// marshal token
	token = sdk.MarshalToken(token)

	// return auth token so that it can be reused form a same function
	return token
}

func getLogs(status *sdk.CommitStatus, event *sdk.Event) (string, error) {
	client := &http.Client{}
	var err error
	gatewayURL := os.Getenv("gateway_url")
	// TODO: support logs for different commit status contexts
	url := fmt.Sprintf("%s/function/pipeline-log?repoPath=%s/%s&commitSHA=%s&function=%s", gatewayURL, event.Owner, event.Repository, event.SHA, event.Service)
	log.Println(url)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	responsePayload, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	return string(responsePayload), nil
}

func reportToGithub(commitStatus *sdk.CommitStatus, event *sdk.Event) error {
	secretKey, err := sdk.ReadSecret(defaultPayloadSecretName)
	if err != nil {
		log.Printf("reusing provided auth token")
		log.Printf("Error reading secretKey: %v", err)
		return err
	}
	privateKey, err := sdk.ReadSecret(defaultPrivateKeyName)
	if err != nil {
		log.Printf("Error reading privateKey: %v", err)
		return err
	}

	appID := os.Getenv("github_app_id")
	cfg := config.Config{
		SecretKey:     secretKey,
		PrivateKey:    privateKey,
		ApplicationID: appID,
	}
	if os.Getenv("use_checks") == "false" {
		return reportStatus(commitStatus.Status, commitStatus.Description, appID, event, cfg)
	}
	return reportCheck(commitStatus, event, cfg)
}

func reportStatus(status string, desc string, statusContext string, event *sdk.Event, cfg config.Config) error {
	appID := os.Getenv("github_app_id")

	ctx := context.Background()

	url := buildPublicStatusURL(status, statusContext, event)

	repoStatus := buildStatus(status, desc, statusContext, url)

	log.Printf("Status: %s, Context: %s, GitHub AppID: %s, Repo: %s, Owner: %s", status, statusContext, appID, event.Repository, event.Owner)

	client := factory.MakeClient(ctx, token, cfg)

	_, _, apiErr := client.Repositories.CreateStatus(ctx, event.Owner, event.Repository, event.SHA, repoStatus)
	if apiErr != nil {
		return fmt.Errorf("failed to report status %v, error: %s", repoStatus, apiErr.Error())
	}

	return nil
}

func reportCheck(commitStatus *sdk.CommitStatus, event *sdk.Event, cfg config.Config) error {
	ctx := context.Background()
	appID := os.Getenv("github_app_id")
	status := commitStatus.Status
	url := buildPublicStatusURL(commitStatus.Status, commitStatus.Context, event)

	log.Printf("Check: %s, Context: %s, GitHub AppID: %s, Repo: %s, Owner: %s", status, commitStatus.Context, appID, event.Repository, event.Owner)

	client := factory.MakeClient(ctx, token, cfg)

	now := github.Timestamp{time.Now()}

	logs, err := getLogs(commitStatus, event)
	if err != nil {
		return err
	}

	var logValue string

	if len(logs) > 0 {
		const maxCheckMessageLength = 65535
		logValue = formatLog(logs, maxCheckMessageLength)
	}

	checks, _, _ := client.Checks.ListCheckRunsForRef(ctx, event.Owner, event.Repository, event.SHA, &github.ListCheckRunsOptions{CheckName: &appID})

	checkRunStatus := getCheckRunStatus(&status)
	conclusion := getCheckRunConclusion(&status)
	summary := getCheckRunDescription(commitStatus, &url)
	log.Printf("Check run status: %s", checkRunStatus)

	var apiErr error
	if *checks.Total == 0 {
		check := github.CreateCheckRunOptions{
			StartedAt: &now,
			Name:      commitStatus.Context,
			HeadSHA:   event.SHA,
			Status:    &checkRunStatus,
			Output: &github.CheckRunOutput{
				Text:    &logValue,
				Title:   getCheckRunTitle(commitStatus),
				Summary: summary,
			},
		}

		if checkRunStatus == githubCheckCompleted {
			check.Conclusion = &conclusion
			check.CompletedAt = &now
		}
		log.Printf("Creating check run %s", check.Name)
		_, _, apiErr = client.Checks.CreateCheckRun(ctx, event.Owner, event.Repository, check)
	} else {
		check := github.UpdateCheckRunOptions{
			Name:       *checks.CheckRuns[0].Name,
			DetailsURL: &url,
			Output: &github.CheckRunOutput{
				Text:    &logValue,
				Title:   getCheckRunTitle(commitStatus),
				Summary: summary,
			},
		}
		if checkRunStatus == "completed" {
			check.Conclusion = &conclusion
			check.CompletedAt = &now
		}
		_, _, apiErr = client.Checks.UpdateCheckRun(ctx, event.Owner, event.Repository, *checks.CheckRuns[0].ID, check)
		log.Printf("Creating check run %s", check.Name)
	}
	if apiErr != nil {
		return fmt.Errorf("failed to report status %s, error: %s", status, apiErr.Error())
	}
	return nil
}

// getCheckRunStatus returns the check run status matching the sdk status
func getCheckRunStatus(status *string) string {
	switch *status {
	case sdk.StatusFailure:
		return githubCheckCompleted
	case sdk.StatusSuccess:
		return githubCheckCompleted
	}
	return githubCheckQueued
}

// getCheckRunConclusion returns the conclusion matching the sdk status
func getCheckRunConclusion(status *string) string {
	switch *status {
	case sdk.StatusFailure:
		return githubConclusionFailure
	case sdk.StatusSuccess:
		return githubConclusionSuccess
	}
	return githubConclusionNeutral
}

// getCheckRunTitle returns a title for the given status to be displayed in Github Checks UI
func getCheckRunTitle(status *sdk.CommitStatus) *string {
	title := status.Description
	switch status.Context {
	case sdk.StackContext:
		title = "Deploy to OpenFaaS"
	default: // Assuming status is either a function name (building) or stack deploy
		title = fmt.Sprintf("Build %s", status.Context)
	}
	return &title
}

// getCheckRunDescription returns a formatted summary for the Check Run page
func getCheckRunDescription(status *sdk.CommitStatus, url *string) *string {
	if status.Status == sdk.StatusSuccess || status.Status == sdk.StatusFailure {
		s := fmt.Sprintf("[%s](%s)", status.Description, *url)
		return &s
	}

	return &status.Description
}

func buildStatus(status string, desc string, context string, url string) *github.RepoStatus {
	return &github.RepoStatus{State: &status, TargetURL: &url, Description: &desc, Context: &context}
}

func truncate(maxLength int, message string) string {
	if len(message) > maxLength {
		message = message[len(message)-maxLength:]
	}
	return message
}

// formatLog formats the logs for the GitHub Checks API including truncating
// to maxCheckMessageLength using the tail of the message.
func formatLog(logs string, maxCheckMessageLength int) string {

	frame := "\n```shell\n%s\n```\n"
	// Remove 2 for the %s

	log.Printf("formatLog: %d bytes", len(logs))

	var logValue string

	if len(logs)+len(frame)-2 > maxCheckMessageLength {
		warning := fmt.Sprintf("Warning: log size (%d) bytes exceeded (%d) bytes so was truncated. See dashboard for full logs.\n\n", len(logs), maxCheckMessageLength)
		newLength := maxCheckMessageLength - len(warning) - len(frame) - 2

		if newLength <= 0 {
			tailVal := truncate(maxCheckMessageLength, logs)
			logValue = tailVal
		} else {
			tailVal := truncate(newLength, logs)
			logValue = warning + tailVal
		}
	} else {
		logValue = logs
	}

	logValue = fmt.Sprintf(frame, logValue)

	return logValue
}
