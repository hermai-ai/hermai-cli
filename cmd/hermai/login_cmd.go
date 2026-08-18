package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/config"
	"github.com/spf13/cobra"
)

// deviceLoginClientName identifies this surface to the platform when it
// records which client asked for a device code. It is not used for
// enforcement, only observability.
const deviceLoginClientName = "hermai-cli"

// deviceTokenMaxInterval caps how far the poller backs off after repeated
// slow_down responses, so a stalled approval still polls occasionally.
const deviceTokenMaxInterval = 30 * time.Second

// deviceTokenDefaultInterval is used when the server omits interval or
// sends zero, which should not happen but is cheap to guard against.
const deviceTokenDefaultInterval = 5 * time.Second

// deviceTokenDefaultTimeout mirrors the device flow standard of a ten
// minute code lifetime and is used only if the server omits expires_in.
const deviceTokenDefaultTimeout = 10 * time.Minute

// deviceCodeResponse matches .data from POST /v1/device/code.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// deviceTokenResponse matches .data from POST /v1/device/token.
type deviceTokenResponse struct {
	Status string `json:"status"`
	APIKey string `json:"api_key,omitempty"`
}

func newLoginCmd() *cobra.Command {
	var paste bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in and save a platform API key",
		Long: `login opens your browser so you can approve this device from your own
signed in session, then saves the key it receives to the hermai config.
No password ever touches this process.

Pass --paste to type or paste a key by hand instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()

			if paste {
				return newRegistryLoginCmd().RunE(cmd, nil)
			}

			if err := runDeviceLogin(cmd, cfg); err != nil {
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "\nSign in did not finish: %s\n", err)
				fmt.Fprintln(out, "You can also paste a key by hand with hermai registry login")
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&paste, "paste", false, "Paste an API key by hand instead of using the browser")
	return cmd
}

func runDeviceLogin(cmd *cobra.Command, cfg config.Config) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	client := newPlatformClient(cfg.Platform)

	code, err := requestDeviceCode(client)
	if err != nil {
		return fmt.Errorf("starting sign in: %w", err)
	}

	fmt.Fprintln(out, "To sign in, open this page in your browser:")
	fmt.Fprintf(out, "\n  %s\n\n", code.VerificationURL)
	fmt.Fprintf(out, "Then enter this code: %s\n\n", code.UserCode)

	if openBrowser(code.VerificationURL) {
		fmt.Fprintln(out, "Your browser should have opened automatically. Waiting for approval")
	} else {
		fmt.Fprintln(out, "Waiting for approval")
	}

	apiKey, err := pollDeviceToken(ctx, out, client, code, nil)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(apiKey, "hm_sk_") {
		return errors.New("the platform returned a key in an unexpected shape, contact support")
	}

	if err := config.SavePlatformKey(apiKey); err != nil {
		return fmt.Errorf("saving key: %w", err)
	}
	fmt.Fprintf(out, "\nSigned in. Key saved to %s\n", config.ConfigFilePath())
	return nil
}

// requestDeviceCode calls POST /v1/device/code and validates the shape of
// the response before the caller starts polling against it.
func requestDeviceCode(client *platformClient) (deviceCodeResponse, error) {
	body, err := json.Marshal(map[string]string{"client_name": deviceLoginClientName})
	if err != nil {
		return deviceCodeResponse{}, err
	}

	data, err := client.do("POST", "/v1/device/code", bytes.NewReader(body), false, nil)
	if err != nil {
		return deviceCodeResponse{}, err
	}

	var code deviceCodeResponse
	if err := json.Unmarshal(data, &code); err != nil {
		return deviceCodeResponse{}, fmt.Errorf("decoding device code response: %w", err)
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURL == "" {
		return deviceCodeResponse{}, errors.New("device code response missing required fields")
	}
	return code, nil
}

// pollDeviceToken polls POST /v1/device/token at the server supplied
// interval until the request is approved, denied, expired, or the local
// timeout derived from expires_in runs out. sleep is injected so tests can
// run the loop without waiting on real clock time; pass nil to use
// time.Sleep.
func pollDeviceToken(ctx context.Context, out io.Writer, client *platformClient, code deviceCodeResponse, sleep func(time.Duration)) (string, error) {
	if sleep == nil {
		sleep = time.Sleep
	}

	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = deviceTokenDefaultInterval
	}
	timeout := time.Duration(code.ExpiresIn) * time.Second
	if timeout <= 0 {
		timeout = deviceTokenDefaultTimeout
	}
	deadline := time.Now().Add(timeout)

	reqBody, err := json.Marshal(map[string]string{"device_code": code.DeviceCode})
	if err != nil {
		return "", err
	}

	dots := 0
	for {
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for approval, run hermai login again")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}

		sleep(interval)
		fmt.Fprint(out, ".")
		dots++
		if dots%40 == 0 {
			fmt.Fprintln(out)
		}

		data, err := client.do("POST", "/v1/device/token", bytes.NewReader(reqBody), false, nil)
		if err != nil {
			return "", friendlyDeviceTokenError(err)
		}

		var token deviceTokenResponse
		if err := json.Unmarshal(data, &token); err != nil {
			return "", fmt.Errorf("decoding token response: %w", err)
		}

		switch token.Status {
		case "approved":
			if token.APIKey == "" {
				return "", errors.New("the platform approved this request but did not return a key")
			}
			fmt.Fprintln(out)
			return token.APIKey, nil
		case "slow_down":
			interval += deviceTokenDefaultInterval
			if interval > deviceTokenMaxInterval {
				interval = deviceTokenMaxInterval
			}
		case "pending":
			// Keep polling at the current interval.
		default:
			return "", fmt.Errorf("unexpected status %q from the platform", token.Status)
		}
	}
}

// friendlyDeviceTokenError turns a typed platform error into plain wording
// a person can act on. Unrecognized codes fall through with their original
// message rather than being swallowed.
func friendlyDeviceTokenError(err error) error {
	var apiErr *platformAPIError
	if !errors.As(err, &apiErr) {
		return err
	}
	code := strings.ToUpper(apiErr.Code)
	switch {
	case strings.Contains(code, "EXPIRE"):
		return errors.New("this code expired before it was approved, run hermai login again")
	case strings.Contains(code, "DENIED"):
		return errors.New("the sign in was denied, run hermai login again if that was not you")
	case strings.Contains(code, "REDEEM") || strings.Contains(code, "USED"):
		return errors.New("this code was already used, run hermai login again to get a new one")
	default:
		return err
	}
}

// openBrowser makes a best effort attempt to open url in the person's
// default browser. Failures are silent and non fatal, the caller always
// prints the URL so a person can open it by hand regardless.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start() == nil
}
