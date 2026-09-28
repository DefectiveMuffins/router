package httputil

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/trafficcapture"
)

type trafficCaptureRoundTripper struct {
	transport http.RoundTripper
}

func (t *trafficCaptureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	scope := trafficcapture.FromContext(request.Context())
	if scope == nil {
		return t.transport.RoundTrip(request)
	}

	startedAt := time.Now()
	exchangeID, attempt := scope.NextAttempt()
	requestBody := captureRequestBody(request)
	exchangeRequest := trafficcapture.Request{
		Method:           request.Method,
		URL:              request.URL.String(),
		Proto:            request.Proto,
		Header:           cloneHTTPHeaders(request.Header),
		ContentLength:    request.ContentLength,
		TransferEncoding: append([]string(nil), request.TransferEncoding...),
	}

	response, err := t.transport.RoundTrip(request)
	capturedRequestBody, requestBodyComplete, bodyErr := requestBody.snapshot()
	exchangeRequest.Body = capturedRequestBody
	exchangeRequest.BodyComplete = requestBodyComplete
	if err != nil {
		captureError := err.Error()
		if bodyErr != nil {
			captureError += "; request body capture failed: " + bodyErr.Error()
		}
		recordUpstreamExchange(request.Context(), scope, trafficcapture.Exchange{
			SchemaVersion: 1,
			ID:            exchangeID,
			ParentID:      scope.ExchangeID(),
			Attempt:       attempt,
			Direction:     trafficcapture.DirectionUpstream,
			StartedAt:     startedAt,
			DurationMS:    time.Since(startedAt).Milliseconds(),
			Request:       exchangeRequest,
			Error:         captureError,
		})
		return nil, err
	}

	responseHeaders := cloneHTTPHeaders(response.Header)
	responseRecord := trafficcapture.Response{
		Proto:            response.Proto,
		StatusCode:       response.StatusCode,
		Header:           responseHeaders,
		ContentLength:    response.ContentLength,
		TransferEncoding: append([]string(nil), response.TransferEncoding...),
	}
	if response.Body == nil || response.Body == http.NoBody {
		recordUpstreamExchange(request.Context(), scope, trafficcapture.Exchange{
			SchemaVersion: 1,
			ID:            exchangeID,
			ParentID:      scope.ExchangeID(),
			Attempt:       attempt,
			Direction:     trafficcapture.DirectionUpstream,
			StartedAt:     startedAt,
			DurationMS:    time.Since(startedAt).Milliseconds(),
			Request:       exchangeRequest,
			Response:      &responseRecord,
			Complete:      exchangeRequest.BodyComplete,
		})
		return response, nil
	}

	response.Body = &trafficCaptureBody{
		ReadCloser: response.Body,
		finish: func(body []byte, complete bool, readErr error) {
			responseRecord.Body = body
			exchange := trafficcapture.Exchange{
				SchemaVersion: 1,
				ID:            exchangeID,
				ParentID:      scope.ExchangeID(),
				Attempt:       attempt,
				Direction:     trafficcapture.DirectionUpstream,
				StartedAt:     startedAt,
				DurationMS:    time.Since(startedAt).Milliseconds(),
				Request:       exchangeRequest,
				Response:      &responseRecord,
				Complete:      complete && exchangeRequest.BodyComplete,
			}
			if requestBodyErr := requestBody.readError(); requestBodyErr != nil {
				exchange.Error = "read upstream request body for capture: " + requestBodyErr.Error()
			}
			if readErr != nil {
				exchange.Error = "read upstream response body: " + readErr.Error()
			}
			recordUpstreamExchange(request.Context(), scope, exchange)
		},
	}
	return response, nil
}

func captureRequestBody(request *http.Request) *requestBodyCapture {
	capture := &requestBodyCapture{}
	if request.Body == nil || request.Body == http.NoBody {
		capture.complete = true
		return capture
	}
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			capture.readErr = err
			return capture
		}
		defer body.Close()
		contents, err := io.ReadAll(body)
		capture.body = contents
		capture.complete = err == nil
		capture.readErr = err
		return capture
	}
	request.Body = &trafficCaptureBody{
		ReadCloser: request.Body,
		finish:     capture.finish,
	}
	return capture
}

type requestBodyCapture struct {
	mu       sync.Mutex
	body     []byte
	complete bool
	readErr  error
}

func (c *requestBodyCapture) finish(body []byte, complete bool, readErr error) {
	c.mu.Lock()
	c.body = body
	c.complete = complete
	c.readErr = readErr
	c.mu.Unlock()
}

func (c *requestBodyCapture) snapshot() ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.body...), c.complete, c.readErr
}

func (c *requestBodyCapture) readError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

func cloneHTTPHeaders(headers http.Header) map[string][]string {
	cloned := make(map[string][]string, len(headers))
	for name, values := range headers {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func recordUpstreamExchange(ctx context.Context, scope *trafficcapture.Scope, exchange trafficcapture.Exchange) {
	if err := scope.Recorder().Record(exchange); err != nil {
		observability.FromContext(ctx).Error("Failed to record local HTTP traffic", "exchange_id", exchange.ID, "parent_exchange_id", exchange.ParentID, "direction", trafficcapture.DirectionUpstream, "err", err)
	}
}

type trafficCaptureBody struct {
	io.ReadCloser
	mu       sync.Mutex
	body     bytes.Buffer
	complete bool
	readErr  error
	once     sync.Once
	finish   func([]byte, bool, error)
}

func (b *trafficCaptureBody) Read(p []byte) (int, error) {
	count, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if count > 0 {
		_, _ = b.body.Write(p[:count])
	}
	if err == io.EOF {
		b.complete = true
	} else if err != nil {
		b.readErr = err
	}
	b.mu.Unlock()
	if err == io.EOF || err != nil {
		b.finalize()
	}
	return count, err
}

func (b *trafficCaptureBody) Close() error {
	err := b.ReadCloser.Close()
	if err != nil {
		b.mu.Lock()
		b.readErr = err
		b.mu.Unlock()
	}
	b.finalize()
	return err
}

func (b *trafficCaptureBody) finalize() {
	b.once.Do(func() {
		b.mu.Lock()
		body := append([]byte(nil), b.body.Bytes()...)
		complete := b.complete
		readErr := b.readErr
		b.mu.Unlock()
		b.finish(body, complete, readErr)
	})
}
