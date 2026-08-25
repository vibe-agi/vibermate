package exchange

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func applyRequestMessageTransform(
	ctx context.Context,
	turn *messagetransform.Turn,
	method string,
	path string,
	headers http.Header,
	body []byte,
) (http.Header, []byte, error) {
	if turn == nil || !turn.HasRequest() {
		return headers.Clone(), bytes.Clone(body), nil
	}
	logicalHeaders, logicalBody, err := logicalTransformInput(headers, body)
	if err != nil {
		return nil, nil, err
	}
	visible, protected := hideCredentialHeaders(logicalHeaders)
	transformed, err := turn.ApplyRequest(ctx, messagetransform.RequestMessage{
		Method:  method,
		Path:    path,
		Headers: visible,
		Body:    logicalBody,
	})
	if err != nil {
		return nil, nil, err
	}
	restoreCredentialHeaders(transformed.Headers, protected)
	return transformed.Headers, transformed.Body, nil
}

func applyResponseMessageTransform(
	ctx context.Context,
	turn *messagetransform.Turn,
	statusCode int,
	headers http.Header,
	body []byte,
) (messagetransform.ResponseMessage, http.Header, http.Header, error) {
	if turn == nil || !turn.HasResponse() {
		return messagetransform.ResponseMessage{
			StatusCode: statusCode, Headers: headers.Clone(), Body: bytes.Clone(body),
		}, headers.Clone(), nil, nil
	}
	logicalHeaders, logicalBody, err := logicalTransformInput(headers, body)
	if err != nil {
		return messagetransform.ResponseMessage{}, nil, nil, err
	}
	visible, protected := hideCredentialHeaders(logicalHeaders)
	transformed, err := turn.ApplyResponse(ctx, messagetransform.ResponseMessage{
		StatusCode: statusCode,
		Headers:    visible,
		Body:       logicalBody,
	})
	if err != nil {
		return messagetransform.ResponseMessage{}, nil, nil, err
	}
	if transformed.Headers.Get("Content-Encoding") != "" {
		return messagetransform.ResponseMessage{}, nil, nil,
			errors.New("message transform cannot assign Content-Encoding")
	}
	return transformed, visible, protected, nil
}

type streamMessageTransformer struct {
	turn       *messagetransform.Turn
	statusCode int
	headers    http.Header
	decoder    *ssewire.Decoder

	beforeHeaders http.Header
	afterHeaders  http.Header
	protected     http.Header
	headerReady   bool
	finished      bool
	finishErr     error
}

func newStreamMessageTransformer(
	turn *messagetransform.Turn,
	statusCode int,
	headers http.Header,
) (*streamMessageTransformer, error) {
	if turn == nil || !turn.HasResponse() {
		return nil, nil
	}
	logicalHeaders := headers.Clone()
	for _, name := range []string{
		"Content-Encoding", "Content-Length", "Content-MD5", "Digest", "ETag",
		"Transfer-Encoding",
	} {
		logicalHeaders.Del(name)
	}
	visible, protected := hideCredentialHeaders(logicalHeaders)
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		return nil, err
	}
	return &streamMessageTransformer{
		turn: turn, statusCode: statusCode, headers: visible, protected: protected,
		decoder: decoder,
	}, nil
}

// newLogicalTransformStream exposes the decoded representation to JavaScript
// while retaining ownership of the upstream body. The transform output is
// always emitted as identity bytes, so the old Content-Encoding and validators
// are removed by newStreamMessageTransformer before the downstream envelope is
// committed.
func newLogicalTransformStream(
	source io.ReadCloser,
	contentEncoding string,
) (io.ReadCloser, error) {
	if source == nil {
		return nil, errors.New("streaming response Body is unavailable")
	}
	var reader io.Reader = source
	var decoder io.Closer
	switch encoding := strings.ToLower(strings.TrimSpace(contentEncoding)); encoding {
	case "", "identity":
	case "gzip":
		gzipReader, err := gzip.NewReader(source)
		if err != nil {
			return nil, fmt.Errorf("decode gzip streaming response: %w", err)
		}
		reader = gzipReader
		decoder = gzipReader
	case "zstd":
		zstdReader, err := zstd.NewReader(
			source,
			zstd.WithDecoderMaxMemory(maxCompleteResponseBytes),
		)
		if err != nil {
			return nil, fmt.Errorf("decode zstd streaming response: %w", err)
		}
		closer := zstdReader.IOReadCloser()
		reader = closer
		decoder = closer
	default:
		return nil, fmt.Errorf(
			"unsupported streaming response Content-Encoding %q",
			encoding,
		)
	}
	return &logicalTransformStream{
		Reader:  reader,
		source:  source,
		decoder: decoder,
	}, nil
}

type logicalTransformStream struct {
	io.Reader
	source  io.ReadCloser
	decoder io.Closer

	closeOnce sync.Once
	closeErr  error
}

func (stream *logicalTransformStream) Close() error {
	if stream == nil {
		return nil
	}
	stream.closeOnce.Do(func() {
		var decoderErr error
		if stream.decoder != nil {
			decoderErr = stream.decoder.Close()
		}
		stream.closeErr = errors.Join(decoderErr, stream.source.Close())
	})
	return stream.closeErr
}

func (stream *logicalTransformStream) ConfirmSemanticTerminal() {
	if stream == nil {
		return
	}
	if terminal, ok := stream.source.(interface{ ConfirmSemanticTerminal() }); ok {
		terminal.ConfirmSemanticTerminal()
	}
}

func (transformer *streamMessageTransformer) Feed(
	ctx context.Context,
	fragment []byte,
) ([]byte, bool, error) {
	if transformer == nil {
		return bytes.Clone(fragment), false, nil
	}
	events, err := transformer.decoder.Feed(fragment)
	if err != nil {
		return nil, false, err
	}
	var encoded bytes.Buffer
	becameReady := false
	for _, event := range events {
		transformedHeaders := transformer.headers.Clone()
		transformedBody := bytes.Clone(event.Data)
		if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) && transformer.headerReady {
			transformedHeaders = transformer.afterHeaders.Clone()
		} else if !bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
			transformed, transformErr := transformer.turn.ApplyResponse(
				ctx,
				messagetransform.ResponseMessage{
					StatusCode: transformer.statusCode,
					Streaming:  true,
					EventName:  event.Name,
					Headers:    transformer.headers,
					Body:       event.Data,
				},
			)
			if transformErr != nil {
				return nil, false, transformErr
			}
			transformedHeaders = transformed.Headers
			transformedBody = transformed.Body
		}
		if !transformer.headerReady {
			transformer.beforeHeaders = transformer.headers.Clone()
			transformer.afterHeaders = transformedHeaders.Clone()
			transformer.headerReady = true
			becameReady = true
		} else if !equalHeaders(transformer.afterHeaders, transformedHeaders) {
			return nil, false, errors.New(
				"streaming response transform changed Headers after the first event",
			)
		}
		event.Data = transformedBody
		wire, encodeErr := ssewire.Encode(event)
		if encodeErr != nil {
			return nil, false, encodeErr
		}
		_, _ = encoded.Write(wire)
	}
	return encoded.Bytes(), becameReady, nil
}

func (transformer *streamMessageTransformer) Finish() error {
	if transformer == nil {
		return nil
	}
	if transformer.finished {
		return transformer.finishErr
	}
	transformer.finished = true
	transformer.finishErr = transformer.decoder.Finish()
	return transformer.finishErr
}

func (transformer *streamMessageTransformer) Envelope() (ResponseEnvelope, error) {
	if transformer == nil || !transformer.headerReady {
		return ResponseEnvelope{}, errors.New("streaming response transform has no complete event")
	}
	return managedEnvelopeWithTransform(
		ResponseModeEventStream,
		transformer.beforeHeaders,
		transformer.afterHeaders,
	)
}

func (transformer *streamMessageTransformer) OriginalEnvelope() (ResponseEnvelope, error) {
	if transformer == nil || !transformer.headerReady {
		return ResponseEnvelope{}, errors.New("streaming response transform has no complete event")
	}
	headers := transformer.afterHeaders.Clone()
	restoreCredentialHeaders(headers, transformer.protected)
	return NewResponseEnvelope(
		ResponseModeEventStream,
		transformer.statusCode,
		headers,
	)
}

func equalHeaders(left, right http.Header) bool {
	if len(left) != len(right) {
		return false
	}
	for name, values := range left {
		if !slices.Equal(values, right.Values(name)) {
			return false
		}
	}
	return true
}

func logicalTransformInput(headers http.Header, body []byte) (http.Header, []byte, error) {
	logicalBody, err := decodeBoundedContent(body, headers.Get("Content-Encoding"))
	if err != nil {
		return nil, nil, fmt.Errorf("decode logical message Body: %w", err)
	}
	logicalHeaders := headers.Clone()
	for _, name := range []string{
		"Content-Encoding", "Content-Length", "Content-MD5", "Digest", "ETag",
		"Transfer-Encoding",
	} {
		logicalHeaders.Del(name)
	}
	return logicalHeaders, logicalBody, nil
}

func hideCredentialHeaders(headers http.Header) (http.Header, http.Header) {
	visible := headers.Clone()
	protected := make(http.Header)
	for name, values := range headers {
		if !rawevidence.NameIsCredential(name) {
			continue
		}
		protected[name] = slices.Clone(values)
		visible.Del(name)
	}
	return visible, protected
}

func restoreCredentialHeaders(headers http.Header, protected http.Header) {
	for name := range headers {
		if rawevidence.NameIsCredential(name) {
			headers.Del(name)
		}
	}
	for name, values := range protected {
		headers[name] = slices.Clone(values)
	}
}

func managedEnvelopeWithTransform(
	mode ResponseMode,
	before http.Header,
	after http.Header,
) (ResponseEnvelope, error) {
	envelope := managedResponseEnvelope(mode)
	names := make(map[string]struct{}, len(before)+len(after))
	for name := range before {
		names[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range after {
		names[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range names {
		beforeValues := before.Values(name)
		afterValues := after.Values(name)
		if slices.Equal(beforeValues, afterValues) {
			continue
		}
		if managedResponseHeaderIsCoreOwned(name) {
			return ResponseEnvelope{}, fmt.Errorf(
				"message transform cannot change Core-owned response Header %q",
				name,
			)
		}
		if len(afterValues) == 0 {
			envelope.headers.Del(name)
			continue
		}
		envelope.headers[name] = slices.Clone(afterValues)
	}
	return NewResponseEnvelope(mode, http.StatusOK, envelope.headers)
}

func managedResponseHeaderIsCoreOwned(name string) bool {
	normalized := strings.ToLower(name)
	return normalized == "cache-control" ||
		normalized == "content-type" ||
		normalized == "content-encoding" ||
		normalized == "content-length" ||
		normalized == "transfer-encoding" ||
		strings.HasPrefix(normalized, "x-vibermate-")
}
