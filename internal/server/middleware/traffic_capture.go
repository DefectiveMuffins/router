package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/trafficcapture"

	"github.com/gin-gonic/gin"
)

const (
	trafficCaptureAnthropicMessagesPath = "/v1/messages"
	trafficCaptureOpenAIChatPath        = "/v1/chat/completions"
	trafficCaptureOpenAIResponsesPath   = "/v1/responses"
	trafficCaptureGeminiModelsPrefix    = "/v1beta/models/"
	trafficCaptureRoutePrefix           = "/v1/route"
)

// WithTrafficCapture records conversation HTTP exchanges without changing the
// body or streaming behavior seen by handlers and clients.
func WithTrafficCapture(recorder trafficcapture.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isConversationRequest(c.Request.Method, c.Request.URL.Path) {
			c.Next()
			return
		}

		startedAt := time.Now()
		exchangeID := trafficcapture.NewID()
		requestBody := &inboundRequestBodyCapture{}
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			requestBody.complete = true
		} else {
			c.Request.Body = &inboundRequestBodyReader{ReadCloser: c.Request.Body, capture: requestBody}
		}
		c.Request = c.Request.WithContext(trafficcapture.WithRecorder(c.Request.Context(), recorder, exchangeID))

		request := trafficcapture.Request{
			Method:           c.Request.Method,
			URL:              c.Request.URL.String(),
			Proto:            c.Request.Proto,
			Header:           cloneHeaders(c.Request.Header),
			ContentLength:    c.Request.ContentLength,
			TransferEncoding: append([]string(nil), c.Request.TransferEncoding...),
		}
		responseWriter := &trafficCaptureResponseWriter{ResponseWriter: c.Writer}
		c.Writer = responseWriter
		c.Next()

		request.Body = append([]byte(nil), requestBody.body.Bytes()...)
		request.BodyComplete = requestBody.complete

		statusCode := responseWriter.Status()
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		responseHeaders := cloneHeaders(c.Writer.Header())
		responseContentLength := int64(-1)
		if length, err := strconv.ParseInt(c.Writer.Header().Get("Content-Length"), 10, 64); err == nil {
			responseContentLength = length
		}
		exchange := trafficcapture.Exchange{
			SchemaVersion: 1,
			ID:            exchangeID,
			Direction:     trafficcapture.DirectionInbound,
			StartedAt:     startedAt,
			DurationMS:    time.Since(startedAt).Milliseconds(),
			Request:       request,
			Response: &trafficcapture.Response{
				Proto:         c.Request.Proto,
				StatusCode:    statusCode,
				Header:        responseHeaders,
				ContentLength: responseContentLength,
				Body:          append([]byte(nil), responseWriter.body.Bytes()...),
			},
			Complete: requestBody.complete,
		}
		if requestBody.readErr != nil {
			exchange.Error = "read inbound request body: " + requestBody.readErr.Error()
		}
		if err := recorder.Record(exchange); err != nil {
			observability.FromGin(c).Error("Failed to record local HTTP traffic", "exchange_id", exchangeID, "direction", trafficcapture.DirectionInbound, "err", err)
		}
	}
}

type inboundRequestBodyCapture struct {
	body     bytes.Buffer
	complete bool
	readErr  error
}

type inboundRequestBodyReader struct {
	io.ReadCloser
	capture *inboundRequestBodyCapture
}

func (r *inboundRequestBodyReader) Read(p []byte) (int, error) {
	count, err := r.ReadCloser.Read(p)
	if count > 0 {
		_, _ = r.capture.body.Write(p[:count])
	}
	if err == io.EOF {
		r.capture.complete = true
	} else if err != nil {
		r.capture.readErr = err
	}
	return count, err
}

func isConversationRequest(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	return path == trafficCaptureAnthropicMessagesPath ||
		strings.HasPrefix(path, trafficCaptureAnthropicMessagesPath+"/") ||
		path == trafficCaptureOpenAIChatPath ||
		path == trafficCaptureOpenAIResponsesPath ||
		strings.HasPrefix(path, trafficCaptureGeminiModelsPrefix) ||
		path == trafficCaptureRoutePrefix ||
		strings.HasPrefix(path, trafficCaptureRoutePrefix+"/")
}

func cloneHeaders(headers http.Header) map[string][]string {
	cloned := make(map[string][]string, len(headers))
	for name, values := range headers {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

type trafficCaptureResponseWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *trafficCaptureResponseWriter) Write(body []byte) (int, error) {
	written, err := w.ResponseWriter.Write(body)
	if written > 0 {
		_, _ = w.body.Write(body[:written])
	}
	return written, err
}

func (w *trafficCaptureResponseWriter) WriteString(body string) (int, error) {
	return w.Write([]byte(body))
}
