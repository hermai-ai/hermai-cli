package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hermai-ai/hermai-cli/pkg/config"
)

func writeJSONResponse(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

func noSleep(time.Duration) {}

func TestPollDeviceTokenPendingThenSlowDownThenApproved(t *testing.T) {
	var calls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/device/token" {
			t.Fatalf("path = %q, want /v1/device/token", r.URL.Path)
		}
		n := atomic.AddInt32(&calls, 1)
		switch n {
		case 1:
			writeJSONResponse(t, w, map[string]any{
				"success": true,
				"data":    map[string]any{"status": "pending"},
			})
		case 2:
			writeJSONResponse(t, w, map[string]any{
				"success": true,
				"data":    map[string]any{"status": "slow_down"},
			})
		default:
			writeJSONResponse(t, w, map[string]any{
				"success": true,
				"data":    map[string]any{"status": "approved", "api_key": "hm_sk_test_123"},
			})
		}
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	code := deviceCodeResponse{
		DeviceCode: "hm_dc_test",
		UserCode:   "ABCD-1234",
		Interval:   1,
		ExpiresIn:  600,
	}

	var out bytes.Buffer
	key, err := pollDeviceToken(context.Background(), &out, client, code, noSleep)
	if err != nil {
		t.Fatalf("pollDeviceToken: %v", err)
	}
	if key != "hm_sk_test_123" {
		t.Fatalf("key = %q, want hm_sk_test_123", key)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3 (pending, slow_down, approved)", got)
	}
}

func TestPollDeviceTokenDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSONResponse(t, w, map[string]any{
			"success": false,
			"error":   map[string]any{"code": "DEVICE_CODE_DENIED", "message": "the request was denied"},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	code := deviceCodeResponse{DeviceCode: "hm_dc_test", UserCode: "ABCD-1234", Interval: 1, ExpiresIn: 600}

	var out bytes.Buffer
	_, err := pollDeviceToken(context.Background(), &out, client, code, noSleep)
	if err == nil {
		t.Fatal("expected an error for a denied code")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %q, want a plain denied explanation", err.Error())
	}
	if strings.Contains(err.Error(), "DEVICE_CODE_DENIED") {
		t.Fatalf("error = %q, should not leak the raw error code", err.Error())
	}
}

func TestPollDeviceTokenExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSONResponse(t, w, map[string]any{
			"success": false,
			"error":   map[string]any{"code": "DEVICE_CODE_EXPIRED", "message": "the code expired"},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	code := deviceCodeResponse{DeviceCode: "hm_dc_test", UserCode: "ABCD-1234", Interval: 1, ExpiresIn: 600}

	var out bytes.Buffer
	_, err := pollDeviceToken(context.Background(), &out, client, code, noSleep)
	if err == nil {
		t.Fatal("expected an error for an expired code")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error = %q, want a plain expired explanation", err.Error())
	}
}

func TestPollDeviceTokenAlreadyRedeemed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSONResponse(t, w, map[string]any{
			"success": false,
			"error":   map[string]any{"code": "DEVICE_CODE_ALREADY_REDEEMED", "message": "already redeemed"},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	code := deviceCodeResponse{DeviceCode: "hm_dc_test", UserCode: "ABCD-1234", Interval: 1, ExpiresIn: 600}

	var out bytes.Buffer
	_, err := pollDeviceToken(context.Background(), &out, client, code, noSleep)
	if err == nil {
		t.Fatal("expected an error for an already redeemed code")
	}
	if !strings.Contains(err.Error(), "already used") {
		t.Fatalf("error = %q, want a plain already used explanation", err.Error())
	}
}

func TestPollDeviceTokenTimesOutLocally(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, map[string]any{
			"success": true,
			"data":    map[string]any{"status": "pending"},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	// expires_in of 0 falls back to the package default, so give an
	// explicit tiny window and let real time pass since the deadline
	// check runs before the (stubbed) sleep on every loop iteration.
	code := deviceCodeResponse{DeviceCode: "hm_dc_test", UserCode: "ABCD-1234", Interval: 1, ExpiresIn: 1}

	var out bytes.Buffer
	slept := 0
	sleep := func(d time.Duration) {
		slept++
		if slept > 3 {
			time.Sleep(1100 * time.Millisecond)
		}
	}
	_, err := pollDeviceToken(context.Background(), &out, client, code, sleep)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, want a timeout explanation", err.Error())
	}
}

func TestPollDeviceTokenRespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, map[string]any{
			"success": true,
			"data":    map[string]any{"status": "pending"},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	code := deviceCodeResponse{DeviceCode: "hm_dc_test", UserCode: "ABCD-1234", Interval: 1, ExpiresIn: 600}

	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	calls := 0
	sleep := func(time.Duration) {
		calls++
		if calls == 2 {
			cancel()
		}
	}
	_, err := pollDeviceToken(ctx, &out, client, code, sleep)
	if err == nil {
		t.Fatal("expected an error once the context is cancelled")
	}
}

func TestRequestDeviceCodeValidatesShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/device/code" {
			t.Fatalf("path = %q, want /v1/device/code", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["client_name"] != "hermai-cli" {
			t.Fatalf("client_name = %q, want hermai-cli", body["client_name"])
		}
		writeJSONResponse(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"device_code":      "hm_dc_abc",
				"user_code":        "ABCD-1234",
				"verification_url": "https://hermai.ai/device?code=ABCD-1234",
				"expires_in":       600,
				"interval":         5,
			},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	code, err := requestDeviceCode(client)
	if err != nil {
		t.Fatalf("requestDeviceCode: %v", err)
	}
	if code.DeviceCode != "hm_dc_abc" || code.UserCode != "ABCD-1234" {
		t.Fatalf("unexpected code response: %+v", code)
	}
}

func TestRequestDeviceCodeRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, map[string]any{
			"success": true,
			"data":    map[string]any{"user_code": "ABCD-1234"},
		})
	}))
	defer server.Close()

	client := newPlatformClient(config.PlatformConfig{URL: server.URL})
	if _, err := requestDeviceCode(client); err == nil {
		t.Fatal("expected an error when device_code is missing")
	}
}
