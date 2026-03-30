// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package robin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// newTestRobin creates a Robin instance pointing at the given test server URL.
func newTestRobin() *Robin {
	config := DefaultRobinClientConfig()
	return &Robin{
		Logger: logr.Discard(),
		httpClient: &http.Client{
			Timeout: config.HTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		config: config,
	}
}

func TestDoGet_Success(t *testing.T) {
	expected := map[string]string{"status": "ok"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer srv.Close()

	robin := newTestRobin()
	body, err := robin.doGet(context.Background(), srv.URL+"/test")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var result map[string]string
	if jsonErr := json.Unmarshal(body, &result); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if result["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", result["status"])
	}
}

func TestDoGet_Returns_RobinHTTPError_On500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	robin := newTestRobin()
	_, err := robin.doGet(context.Background(), srv.URL+"/test")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var httpErr *RobinHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected RobinHTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status code 500, got %d", httpErr.StatusCode)
	}
	if httpErr.Body != "internal server error" {
		t.Errorf("expected body 'internal server error', got %q", httpErr.Body)
	}
}

func TestDoPut_Returns_RobinHTTPError_On4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	robin := newTestRobin()
	payload := []byte(`{"key":"value"}`)
	_, err := robin.doPut(context.Background(), srv.URL+"/test", payload)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var httpErr *RobinHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected RobinHTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status code 400, got %d", httpErr.StatusCode)
	}
}

func TestRetry_Succeeds_On_Second_Attempt(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("temporarily unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"success"}`))
	}))
	defer srv.Close()

	robin := newTestRobin()
	body, err := robin.doWithRetry(context.Background(), func() ([]byte, error) {
		return robin.doGet(context.Background(), srv.URL+"/test")
	})
	if err != nil {
		t.Fatalf("expected no error after retry, got %v", err)
	}

	var result map[string]string
	if jsonErr := json.Unmarshal(body, &result); jsonErr != nil {
		t.Fatalf("failed to unmarshal response: %v", jsonErr)
	}
	if result["result"] != "success" {
		t.Errorf("expected result 'success', got %q", result["result"])
	}
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 calls, got %d", atomic.LoadInt32(&callCount))
	}
}

func TestRetry_Does_Not_Retry_On_400(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	robin := newTestRobin()
	_, err := robin.doWithRetry(context.Background(), func() ([]byte, error) {
		return robin.doGet(context.Background(), srv.URL+"/test")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var httpErr *RobinHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected RobinHTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status code 400, got %d", httpErr.StatusCode)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected exactly 1 call (no retry for 400), got %d", atomic.LoadInt32(&callCount))
	}
}

func TestContext_Cancellation_Aborts_Request(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	robin := newTestRobin()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := robin.doGet(ctx, srv.URL+"/test")
	if err == nil {
		t.Fatal("expected error due to context cancellation, got nil")
	}
}

func TestDoPut_BugFix_ErrorBeforeHeaderSet(t *testing.T) {
	// Test that doPut handles an invalid URL gracefully (error returned before
	// attempting to set headers on a nil request).
	robin := newTestRobin()
	_, err := robin.doPut(context.Background(), "://bad-url", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
	// The key assertion: this should NOT panic. Before the bug fix,
	// req.Header.Set was called before the error check, which would panic
	// if http.NewRequestWithContext returned a nil request.
}

func TestIsRetryable_ServerError(t *testing.T) {
	err := &RobinHTTPError{StatusCode: 503, Body: "unavailable", URL: "/test"}
	if !IsRetryable(err) {
		t.Error("expected 503 to be retryable")
	}
}

func TestIsRetryable_ClientError(t *testing.T) {
	err := &RobinHTTPError{StatusCode: 400, Body: "bad request", URL: "/test"}
	if IsRetryable(err) {
		t.Error("expected 400 to NOT be retryable")
	}
}

func TestIsRetryable_NetworkError(t *testing.T) {
	err := errors.New("connection refused")
	if !IsRetryable(err) {
		t.Error("expected network error to be retryable")
	}
}

func TestRobinHTTPError_ErrorMessage(t *testing.T) {
	err := &RobinHTTPError{StatusCode: 404, Body: "not found", URL: "http://localhost/test"}
	expected := "robin HTTP error: status 404 from http://localhost/test: not found"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestDefaultRobinClientConfig(t *testing.T) {
	config := DefaultRobinClientConfig()
	if config.HTTPTimeout != 30*time.Second {
		t.Errorf("expected HTTPTimeout 30s, got %v", config.HTTPTimeout)
	}
	if config.MaxRetries != 3 {
		t.Errorf("expected MaxRetries 3, got %d", config.MaxRetries)
	}
	if config.RetryBackoff != 500*time.Millisecond {
		t.Errorf("expected RetryBackoff 500ms, got %v", config.RetryBackoff)
	}
}

func TestDoWithRetryNetworkOnly_DoesNotRetryHTTPError(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	robin := newTestRobin()
	_, err := robin.doWithRetryNetworkOnly(context.Background(), func() ([]byte, error) {
		return robin.doGet(context.Background(), srv.URL+"/test")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// doWithRetryNetworkOnly should NOT retry on HTTP errors (even 5xx)
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected exactly 1 call (no retry for HTTP errors in network-only mode), got %d", atomic.LoadInt32(&callCount))
	}
}
