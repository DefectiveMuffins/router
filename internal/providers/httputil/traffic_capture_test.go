package httputil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/trafficcapture"
)

type capturedExchanges struct {
	exchanges []trafficcapture.Exchange
}

func (r *capturedExchanges) Record(exchange trafficcapture.Exchange) error {
	r.exchanges = append(r.exchanges, exchange)
	return nil
}

func TestNewClientCapturesUpstreamRequestAndStreamedResponse(t *testing.T) {
	recorder := &capturedExchanges{}
	requestPayload := []byte(`{"model":"test","stream":true}`)
	responsePayload := []byte("data: first\n\ndata: second\n\n")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		actualRequestBody, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(actualRequestBody, requestPayload) {
			t.Fatalf("provider received request body %q, want %q", actualRequestBody, requestPayload)
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
			ContentLength: int64(len(responsePayload)),
			Body:          io.NopCloser(bytes.NewReader(responsePayload)),
		}, nil
	})
	client := NewClient(transport)
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-1")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", bytes.NewReader(requestPayload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test", "visible")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	actualResponseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualResponseBody, responsePayload) {
		t.Fatalf("client received response body %q, want %q", actualResponseBody, responsePayload)
	}
	if len(recorder.exchanges) != 1 {
		t.Fatalf("captured exchanges = %d, want 1", len(recorder.exchanges))
	}
	exchange := recorder.exchanges[0]
	if exchange.Direction != trafficcapture.DirectionUpstream || exchange.ParentID != "inbound-1" || exchange.Attempt != 1 || !exchange.Complete {
		t.Fatalf("captured upstream metadata = %#v", exchange)
	}
	if !bytes.Equal(exchange.Request.Body, requestPayload) || exchange.Request.Header["X-Test"][0] != "visible" {
		t.Fatalf("captured provider request = %#v", exchange.Request)
	}
	if exchange.Response == nil || exchange.Response.StatusCode != http.StatusOK || !bytes.Equal(exchange.Response.Body, responsePayload) {
		t.Fatalf("captured provider response = %#v", exchange.Response)
	}
}

func TestNewClientCapturesEachUpstreamRetrySeparately(t *testing.T) {
	recorder := &capturedExchanges{}
	client := NewClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: http.NoBody}, nil
	}))
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-2")
	for range 2 {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if len(recorder.exchanges) != 2 || recorder.exchanges[0].Attempt != 1 || recorder.exchanges[1].Attempt != 2 {
		t.Fatalf("captured attempts = %#v, want separate attempts 1 and 2", recorder.exchanges)
	}
}

func TestNewClientRecordsTransportFailureWithoutChangingError(t *testing.T) {
	recorder := &capturedExchanges{}
	upstreamErr := errors.New("provider unavailable")
	client := NewClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, upstreamErr
	}))
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-3")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("client error = %v, want wrapped provider error", err)
	}
	if len(recorder.exchanges) != 1 || !strings.Contains(recorder.exchanges[0].Error, upstreamErr.Error()) {
		t.Fatalf("captured transport failure = %#v", recorder.exchanges)
	}
}

func TestNewClientRecordsPartialResponseWhenClientClosesEarly(t *testing.T) {
	recorder := &capturedExchanges{}
	client := NewClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			ContentLength: int64(len("all bytes")),
			Body:          io.NopCloser(strings.NewReader("all bytes")),
		}, nil
	}))
	ctx := trafficcapture.WithRecorder(context.Background(), recorder, "inbound-4")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 3)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(recorder.exchanges) != 1 || recorder.exchanges[0].Complete {
		t.Fatalf("partial response exchange = %#v, want incomplete", recorder.exchanges)
	}
	if string(recorder.exchanges[0].Response.Body) != "all" {
		t.Fatalf("partial response body = %q, want all", recorder.exchanges[0].Response.Body)
	}
}
