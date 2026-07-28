package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type readinessRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip readinessRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestReadinessTimeoutDefaultsAreProfileAwareAndConfigurable(t *testing.T) {
	settings := &Settings{}

	development, err := settings.ReadinessTimeoutFor(NextExecutionDevelopment)
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, development)

	production, err := settings.ReadinessTimeoutFor(NextExecutionProduction)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, production)

	settings.ReadinessTimeout = "3m"
	configured, err := settings.ReadinessTimeoutFor(NextExecutionDevelopment)
	require.NoError(t, err)
	require.Equal(t, 3*time.Minute, configured)
}

func TestReadinessTimeoutRejectsInvalidOrUnboundedValues(t *testing.T) {
	for _, value := range []string{"nope", "0s", "-1s", "11m"} {
		settings := &Settings{ReadinessTimeout: value}
		_, err := settings.ReadinessTimeoutFor(NextExecutionDevelopment)
		require.Error(t, err, value)
	}
}

func TestWaitForHTTPReadyRetriesUntilTheServerResponds(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{
		Transport: readinessRoundTripper(func(_ *http.Request) (*http.Response, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("cold compile in progress")
			}
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("warming")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	err := waitForHTTPReady(
		context.Background(),
		client,
		"http://frontend.test",
		time.Second,
		time.Millisecond,
	)
	require.NoError(t, err)
	require.EqualValues(t, 3, attempts.Load())
}

func TestWaitForHTTPReadyHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{
		Transport: readinessRoundTripper(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		}),
	}

	err := waitForHTTPReady(ctx, client, "http://frontend.test", time.Second, time.Millisecond)
	require.ErrorIs(t, err, context.Canceled)
}

func TestWaitForHTTPReadyReportsTheLastProbeOnTimeout(t *testing.T) {
	client := &http.Client{
		Transport: readinessRoundTripper(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	}

	err := waitForHTTPReady(
		context.Background(),
		client,
		"http://frontend.test",
		10*time.Millisecond,
		time.Millisecond,
	)
	require.ErrorContains(t, err, "not ready after 10ms")
	require.ErrorContains(t, err, "connection refused")
}
