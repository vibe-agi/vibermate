package wirecapture

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const (
	DefaultMaxBodyBytes   = 16 << 20
	DefaultMaxHeaderBytes = 64 << 10
	defaultCaptureTimeout = 30 * time.Second
)

type Options struct {
	TLSConfig      *tls.Config
	SampleLimit    int
	MaxBodyBytes   uint64
	MaxHeaderBytes uint32
	Timeout        time.Duration
	Clock          func() time.Time
}

// Capture accepts loopback TLS connections until SampleLimit safe samples
// have completed. No raw request, TLS record, header value other than
// User-Agent, or request body crosses the returned boundary.
func Capture(
	ctx context.Context,
	listener net.Listener,
	options Options,
) (Report, error) {
	if ctx == nil || listener == nil {
		return Report{}, errors.New("wire capture dependencies are incomplete")
	}
	if err := validateLoopbackListener(listener); err != nil {
		return Report{}, err
	}
	if options.TLSConfig == nil || len(options.TLSConfig.Certificates) == 0 {
		return Report{}, errors.New("wire capture TLS identity is missing")
	}
	if options.SampleLimit <= 0 || options.SampleLimit > 1024 {
		return Report{}, errors.New("wire capture sample limit is invalid")
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if options.MaxHeaderBytes == 0 {
		options.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if options.MaxBodyBytes > DefaultMaxBodyBytes ||
		options.MaxHeaderBytes > DefaultMaxHeaderBytes {
		return Report{}, errors.New("wire capture byte bound exceeds the supported maximum")
	}
	if options.Timeout == 0 {
		options.Timeout = defaultCaptureTimeout
	}
	if options.Timeout < 0 {
		return Report{}, errors.New("wire capture timeout must be positive")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}

	tlsConfig := options.TLSConfig.Clone()
	if tlsConfig.MinVersion == 0 || tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	tlsConfig.NextProtos = []string{"h2", "http/1.1"}

	results := make(chan captureResult, options.SampleLimit)
	acceptErrors := make(chan error, 1)
	owner, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	var workers sync.WaitGroup
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				if owner.Err() == nil {
					acceptErrors <- err
				}
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				sample, captureErr := captureConnection(
					owner,
					connection,
					tlsConfig,
					options,
				)
				select {
				case results <- captureResult{sample: sample, err: captureErr}:
				case <-owner.Done():
				}
			}()
		}
	}()

	report := Report{
		SchemaVersion: ReportSchema,
		CreatedAt:     options.Clock().UTC(),
		Samples:       make([]Sample, 0, options.SampleLimit),
	}
	var failures []error
	for len(report.Samples) < options.SampleLimit {
		select {
		case result := <-results:
			if result.err != nil {
				failures = append(failures, result.err)
				continue
			}
			report.Samples = append(report.Samples, result.sample)
		case err := <-acceptErrors:
			cancel(err)
			_ = listener.Close()
			workers.Wait()
			return Report{}, errors.Join(err, errors.Join(failures...))
		case <-ctx.Done():
			cancel(context.Cause(ctx))
			_ = listener.Close()
			workers.Wait()
			return Report{}, errors.Join(context.Cause(ctx), errors.Join(failures...))
		}
	}
	cancel(errSampleLimitReached)
	_ = listener.Close()
	workers.Wait()
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

var errSampleLimitReached = errors.New("wire capture sample limit reached")

type captureResult struct {
	sample Sample
	err    error
}

func validateLoopbackListener(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return errors.New("wire capture listener must be TCP loopback")
	}
	return nil
}

func captureConnection(
	ctx context.Context,
	connection net.Conn,
	tlsConfig *tls.Config,
	options Options,
) (Sample, error) {
	defer connection.Close()
	deadline := options.Clock().Add(options.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return Sample{}, err
	}
	observation, replay, err := transportprofile.CaptureClientHello(
		ctx,
		connection,
		transportprofile.DefaultMaxClientHelloBytes,
	)
	if err != nil {
		return Sample{}, fmt.Errorf("capture ClientHello: %w", err)
	}
	server := tls.Server(replay, tlsConfig)
	if err := server.HandshakeContext(ctx); err != nil {
		return Sample{}, fmt.Errorf("complete capture TLS handshake: %w", err)
	}
	state := server.ConnectionState()
	observation, err = observation.WithDownstreamNegotiatedALPN(state.NegotiatedProtocol)
	if err != nil {
		return Sample{}, err
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return Sample{}, err
	}
	sample := Sample{
		CapturedAt:     options.Clock().UTC(),
		NegotiatedALPN: state.NegotiatedProtocol,
		TLS:            fingerprint,
	}
	switch state.NegotiatedProtocol {
	case "http/1.1":
		request, captureErr := captureHTTP1(
			server,
			options.MaxHeaderBytes,
			options.MaxBodyBytes,
		)
		if captureErr != nil {
			return Sample{}, captureErr
		}
		sample.UserAgent = request.userAgent
		sample.HeaderOrder = request.headerOrder
		sample.BodyBytes = request.bodyBytes
	case "h2":
		request, captureErr := captureHTTP2(
			server,
			options.MaxHeaderBytes,
			options.MaxBodyBytes,
		)
		if captureErr != nil {
			return Sample{}, captureErr
		}
		sample.UserAgent = request.userAgent
		sample.PseudoHeaderOrder = request.pseudoHeaderOrder
		sample.HeaderOrder = request.headerOrder
		sample.BodyBytes = request.bodyBytes
		sample.HTTP2 = &request.http2
	default:
		return Sample{}, errors.New("wire capture negotiated an unsupported ALPN")
	}
	return sample, sample.validate()
}

type httpObservation struct {
	userAgent         string
	pseudoHeaderOrder []string
	headerOrder       []string
	bodyBytes         uint64
	http2             H2Observation
}

func captureHTTP1(
	connection net.Conn,
	maxHeaderBytes uint32,
	maxBodyBytes uint64,
) (httpObservation, error) {
	reader := bufio.NewReaderSize(connection, int(maxHeaderBytes))
	requestLine, err := readBoundedLine(reader, maxHeaderBytes)
	if err != nil || !strings.HasSuffix(requestLine, " HTTP/1.1") {
		return httpObservation{}, errors.New("wire capture HTTP/1.1 request line is invalid")
	}
	remaining := int64(maxHeaderBytes) - int64(len(requestLine)) - 2
	result := httpObservation{}
	var contentLength uint64
	chunked := false
	for {
		line, lineErr := readBoundedLine(reader, uint32(max(remaining, 0)))
		if lineErr != nil {
			return httpObservation{}, lineErr
		}
		remaining -= int64(len(line)) + 2
		if remaining < 0 {
			return httpObservation{}, errors.New("wire capture HTTP/1.1 headers exceed their bound")
		}
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		name = strings.ToLower(strings.TrimSpace(name))
		if !found || !validHeaderName(name) {
			return httpObservation{}, errors.New("wire capture HTTP/1.1 header is invalid")
		}
		if len(result.headerOrder) >= MaxHeaderFields {
			return httpObservation{}, errors.New("wire capture HTTP/1.1 header count exceeds its bound")
		}
		result.headerOrder = append(result.headerOrder, name)
		value = strings.TrimSpace(value)
		switch name {
		case "user-agent":
			if result.userAgent != "" ||
				validateText("User-Agent", value, MaxUserAgentBytes, false) != nil {
				return httpObservation{}, errors.New("wire capture User-Agent is invalid")
			}
			result.userAgent = value
		case "content-length":
			parsed, parseErr := strconv.ParseUint(value, 10, 64)
			if parseErr != nil {
				return httpObservation{}, errors.New("wire capture Content-Length is invalid")
			}
			contentLength = parsed
		case "transfer-encoding":
			chunked = strings.EqualFold(value, "chunked")
		}
	}
	if chunked && contentLength != 0 {
		return httpObservation{}, errors.New("wire capture request has conflicting body framing")
	}
	if chunked {
		count, copyErr := io.Copy(io.Discard, io.LimitReader(
			httputil.NewChunkedReader(reader),
			int64(maxBodyBytes)+1,
		))
		if copyErr != nil {
			return httpObservation{}, fmt.Errorf("discard chunked capture body: %w", copyErr)
		}
		if uint64(count) > maxBodyBytes {
			return httpObservation{}, errors.New("wire capture body exceeds its bound")
		}
		result.bodyBytes = uint64(count)
	} else {
		if contentLength > maxBodyBytes {
			return httpObservation{}, errors.New("wire capture body exceeds its bound")
		}
		if _, err := io.CopyN(io.Discard, reader, int64(contentLength)); err != nil {
			return httpObservation{}, fmt.Errorf("discard capture body: %w", err)
		}
		result.bodyBytes = contentLength
	}
	responseBody := "{}"
	_, err = io.WriteString(connection,
		"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n"+
			responseBody,
	)
	return result, err
}

func readBoundedLine(reader *bufio.Reader, limit uint32) (string, error) {
	if limit == 0 {
		return "", errors.New("wire capture header byte bound is exhausted")
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) > int(limit)+2 || !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("wire capture HTTP line is invalid")
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= '0' && character <= '9') && character != '-' {
			return false
		}
	}
	return true
}

func captureHTTP2(
	connection net.Conn,
	maxHeaderBytes uint32,
	maxBodyBytes uint64,
) (httpObservation, error) {
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(connection, preface); err != nil ||
		string(preface) != http2.ClientPreface {
		return httpObservation{}, errors.New("wire capture HTTP/2 preface is invalid")
	}
	framer := http2.NewFramer(connection, connection)
	framer.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	framer.MaxHeaderListSize = maxHeaderBytes
	if err := framer.WriteSettings(
		http2.Setting{ID: http2.SettingEnablePush, Val: 0},
		http2.Setting{ID: http2.SettingMaxFrameSize, Val: 16384},
	); err != nil {
		return httpObservation{}, fmt.Errorf("write capture HTTP/2 settings: %w", err)
	}
	result := httpObservation{}
	var streamID uint32
	requestEnded := false
	for !requestEnded {
		frame, err := framer.ReadFrame()
		if err != nil {
			return httpObservation{}, fmt.Errorf("read capture HTTP/2 frame: %w", err)
		}
		switch typed := frame.(type) {
		case *http2.SettingsFrame:
			if typed.IsAck() {
				continue
			}
			if len(result.http2.Settings) != 0 {
				return httpObservation{}, errors.New("wire capture received duplicate initial settings")
			}
			if err := typed.ForeachSetting(func(setting http2.Setting) error {
				result.http2.Settings = append(result.http2.Settings, H2Setting{
					ID:    uint16(setting.ID),
					Value: setting.Val,
				})
				return nil
			}); err != nil {
				return httpObservation{}, err
			}
			if err := framer.WriteSettingsAck(); err != nil {
				return httpObservation{}, err
			}
		case *http2.WindowUpdateFrame:
			if typed.StreamID == 0 && result.http2.ConnectionWindow == 0 {
				result.http2.ConnectionWindow = typed.Increment
			}
		case *http2.PriorityFrame:
			result.http2.PriorityFrameCount++
		case *http2.MetaHeadersFrame:
			if streamID != 0 {
				return httpObservation{}, errors.New("wire capture observed more than one HTTP/2 request")
			}
			streamID = typed.StreamID
			for _, field := range typed.Fields {
				name := strings.ToLower(field.Name)
				if strings.HasPrefix(name, ":") {
					result.pseudoHeaderOrder = append(result.pseudoHeaderOrder, name)
				} else {
					result.headerOrder = append(result.headerOrder, name)
				}
				if len(result.pseudoHeaderOrder)+len(result.headerOrder) > MaxHeaderFields {
					return httpObservation{}, errors.New("wire capture HTTP/2 header count exceeds its bound")
				}
				if name == "user-agent" {
					if result.userAgent != "" ||
						validateText("User-Agent", field.Value, MaxUserAgentBytes, false) != nil {
						return httpObservation{}, errors.New("wire capture User-Agent is invalid")
					}
					result.userAgent = field.Value
				}
			}
			requestEnded = typed.StreamEnded()
		case *http2.DataFrame:
			if streamID == 0 || typed.StreamID != streamID {
				return httpObservation{}, errors.New("wire capture HTTP/2 DATA stream is invalid")
			}
			result.bodyBytes += uint64(len(typed.Data()))
			if result.bodyBytes > maxBodyBytes {
				return httpObservation{}, errors.New("wire capture body exceeds its bound")
			}
			if len(typed.Data()) != 0 {
				increment := uint32(len(typed.Data()))
				if err := framer.WriteWindowUpdate(0, increment); err != nil {
					return httpObservation{}, err
				}
				if err := framer.WriteWindowUpdate(streamID, increment); err != nil {
					return httpObservation{}, err
				}
			}
			requestEnded = typed.StreamEnded()
		case *http2.PingFrame:
			if !typed.Flags.Has(http2.FlagPingAck) {
				if err := framer.WritePing(true, typed.Data); err != nil {
					return httpObservation{}, err
				}
			}
		case *http2.GoAwayFrame:
			return httpObservation{}, errors.New("wire capture peer closed before a request completed")
		}
	}
	if len(result.http2.Settings) == 0 || streamID == 0 {
		return httpObservation{}, errors.New("wire capture HTTP/2 request is incomplete")
	}
	var headerBlock strings.Builder
	encoder := hpack.NewEncoder(&headerBlock)
	for _, field := range []hpack.HeaderField{
		{Name: ":status", Value: "200"},
		{Name: "content-type", Value: "application/json"},
		{Name: "content-length", Value: "2"},
	} {
		if err := encoder.WriteField(field); err != nil {
			return httpObservation{}, err
		}
	}
	if err := framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: []byte(headerBlock.String()),
		EndHeaders:    true,
	}); err != nil {
		return httpObservation{}, err
	}
	if err := framer.WriteData(streamID, true, []byte("{}")); err != nil {
		return httpObservation{}, err
	}
	result.http2.AkamaiFingerprint = akamaiFingerprint(
		result.http2.Settings,
		result.http2.ConnectionWindow,
		result.http2.PriorityFrameCount,
		result.pseudoHeaderOrder,
	)
	result.http2.AkamaiHash = md5Fingerprint(result.http2.AkamaiFingerprint)
	return result, nil
}

func akamaiFingerprint(
	settings []H2Setting,
	window uint32,
	priorities uint32,
	pseudoOrder []string,
) string {
	settingParts := make([]string, 0, len(settings))
	for _, setting := range settings {
		settingParts = append(
			settingParts,
			fmt.Sprintf("%d:%d", setting.ID, setting.Value),
		)
	}
	pseudoParts := make([]string, 0, len(pseudoOrder))
	for _, name := range pseudoOrder {
		switch name {
		case ":method":
			pseudoParts = append(pseudoParts, "m")
		case ":authority":
			pseudoParts = append(pseudoParts, "a")
		case ":scheme":
			pseudoParts = append(pseudoParts, "s")
		case ":path":
			pseudoParts = append(pseudoParts, "p")
		default:
			pseudoParts = append(pseudoParts, "?")
		}
	}
	return strings.Join(settingParts, ";") + "|" +
		strconv.FormatUint(uint64(window), 10) + "|" +
		strconv.FormatUint(uint64(priorities), 10) + "|" +
		strings.Join(pseudoParts, ",")
}
