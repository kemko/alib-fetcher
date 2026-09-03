// Package slink prepares Alib photo links and publishes images to Slink.
package slink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/c-robinson/iplib/v2/iana"
	"github.com/kemko/alib-fetcher/internal/alib"
)

const (
	maxDownloadBytes  = 15 << 20
	maxUploadResponse = 1 << 20
	maxMetaRedirects  = 5
	maxHTTPRedirects  = 5
	userAgent         = "alib-fetcher/1.0"
	logKeyError       = "error"
	logKeyErrorCat    = "error_category"
	logKeyErrorType   = "error_type"
	logKeyIndex       = "index"
	logKeyHTTPStatus  = "http_status"
	logKeyStage       = "stage"
)

// Options configures the HTTP and DNS dependencies of Client.
type Options struct {
	HTTPClient  *http.Client
	LookupIP    func(context.Context, string) ([]net.IP, error)
	DialContext func(context.Context, string, string) (net.Conn, error)
}

// PreparedBook contains a book with successful Slink processing results.
type PreparedBook struct {
	temporaryDirectory string
	cleanup            func() error
	Book               alib.Book
}

// NewPreparedBook creates a prepared result with an optional idempotent cleanup function.
func NewPreparedBook(book alib.Book, cleanup func() error) *PreparedBook {
	return &PreparedBook{Book: book, cleanup: cleanup}
}

// Cleanup removes the temporary files for the book. It is safe to call more than once.
func (p *PreparedBook) Cleanup() error {
	if p == nil {
		return nil
	}
	if p.cleanup != nil {
		return p.cleanup()
	}
	if p.temporaryDirectory == "" {
		return nil
	}

	return os.RemoveAll(p.temporaryDirectory)
}

// TemporaryDirectory returns the book's temporary directory for lifecycle coordination.
func (p *PreparedBook) TemporaryDirectory() string {
	if p == nil {
		return ""
	}

	return p.temporaryDirectory
}

// Client downloads photo files and uploads images to Slink.
type Client struct {
	baseURL *url.URL
	http    *http.Client
	source  *http.Client
	logger  *slog.Logger
	apiKey  string
	tagID   string
	profile string
	timeout time.Duration
}

// NewClient creates a Slink photo processor.
func NewClient(rawBaseURL, apiKey, tagID string, timeout time.Duration, logger *slog.Logger) (*Client, error) {
	return NewClientWithOptions(rawBaseURL, apiKey, tagID, timeout, logger, Options{})
}

// NewClientWithOptions creates a Slink photo processor with injectable HTTP and DNS dependencies.
func NewClientWithOptions(
	rawBaseURL, apiKey, tagID string,
	timeout time.Duration,
	logger *slog.Logger,
	options Options,
) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("create slink client: timeout must be positive")
	}
	if apiKey == "" {
		return nil, errors.New("create slink client: API key is required")
	}
	if !strings.HasPrefix(apiKey, "sk_") {
		return nil, errors.New("create slink client: API key must start with sk_")
	}
	if tagID == "" {
		return nil, errors.New("create slink client: tag ID is required")
	}
	if logger == nil {
		return nil, errors.New("create slink client: logger is required")
	}

	baseURL, err := parseBaseURL(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("create slink client: %w", err)
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	} else {
		copyClient := *httpClient
		httpClient = &copyClient
	}

	lookupIP := options.LookupIP
	if lookupIP == nil {
		lookupIP = defaultLookupIP
	}
	dialContext := options.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("create slink client: default HTTP transport is unavailable")
	}
	sourceTransport := defaultTransport.Clone()
	sourceTransport.Proxy = nil
	sourceTransport.DialContext = secureDialContext(lookupIP, dialContext, timeout)

	profileHash := sha256.Sum256([]byte(baseURL.String() + "\x00" + tagID))
	client := &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		tagID:   tagID,
		profile: "slink:" + hex.EncodeToString(profileHash[:8]),
		timeout: timeout,
		http:    httpClient,
		source:  &http.Client{Transport: sourceTransport, Timeout: timeout},
		logger:  logger,
	}
	client.source.CheckRedirect = client.checkRedirect

	return client, nil
}

// Profile returns the stable identifier for this Slink configuration.
func (c *Client) Profile() string {
	return c.profile
}

// Process downloads and publishes a book's photos in source order.
func (c *Client) Process(ctx context.Context, book alib.Book) (*PreparedBook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prepared := NewPreparedBook(cloneBook(book), nil)
	if len(prepared.Book.Photos) == 0 {
		return prepared, nil
	}

	directory, err := os.MkdirTemp("", "alib-fetcher-photos-")
	if err != nil {
		return nil, fmt.Errorf("create temporary photo directory: %w", err)
	}
	prepared.temporaryDirectory = directory
	prepared.cleanup = func() error { return os.RemoveAll(directory) }
	if prepareErr := c.preparePhotos(ctx, prepared, safeReferer(book.BuyURL)); prepareErr != nil {
		return nil, errors.Join(prepareErr, prepared.Cleanup())
	}

	return prepared, nil
}

func (c *Client) preparePhotos(ctx context.Context, prepared *PreparedBook, referer string) error {
	cache := make(map[string]photoResult)
	for index := range prepared.Book.Photos {
		if err := ctx.Err(); err != nil {
			return err
		}
		photo := &prepared.Book.Photos[index]
		if result, found := cache[photo.URL]; found {
			applyPhotoResult(photo, result)
			continue
		}
		if _, reusable := c.reusableResult(*photo); reusable {
			cache[photo.URL] = photoResultFromPhoto(*photo)
			continue
		}

		result, file, processErr := c.processPhoto(ctx, prepared.TemporaryDirectory(), *photo, referer)
		if processErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logFailure(ctx, index, processErr)
			return processErr
		}
		if file != "" {
			result, processErr = c.publishImage(ctx, file, result.contentType)
			if processErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.logFailure(ctx, index, processErr)
				return processErr
			}
		}
		applyPhotoResult(photo, result)
		cache[photo.URL] = result
	}

	return nil
}

type photoResult struct {
	slinkURL     string
	slinkProfile string
	contentType  string
	nonImage     bool
}

type downloadedFile struct {
	path        string
	contentType string
}

func (c *Client) processPhoto(
	ctx context.Context,
	directory string,
	photo alib.Photo,
	referer string,
) (photoResult, string, error) {
	currentURL := photo.URL
	visited := make(map[string]struct{})
	for metaRedirect := 0; ; metaRedirect++ {
		if err := ctx.Err(); err != nil {
			return photoResult{}, "", err
		}
		if _, found := visited[currentURL]; found {
			return photoResult{}, "", photoFailure("source_meta", "redirect_cycle", 0, nil)
		}
		visited[currentURL] = struct{}{}

		file, responseURL, err := c.download(ctx, directory, currentURL, referer)
		if err != nil {
			return photoResult{}, "", err
		}
		nextURL, found, parseErr := metaRefresh(file.path, responseURL)
		if parseErr != nil {
			return photoResult{}, "", photoFailure("source_meta", "meta_redirect", 0, parseErr)
		}
		if !found {
			if !isImageType(file.contentType) {
				return photoResult{nonImage: true, slinkProfile: c.profile}, "", nil
			}
			return photoResult{contentType: file.contentType}, file.path, nil
		}
		if metaRedirect >= maxMetaRedirects {
			return photoResult{}, "", photoFailure("source_meta", "redirect_limit", 0, nil)
		}
		currentURL = nextURL
	}
}

func (c *Client) download(ctx context.Context, directory, rawURL, referer string) (downloadedFile, *url.URL, error) {
	parsedURL, err := parseSourceURL(rawURL)
	if err != nil {
		return downloadedFile{}, nil, photoFailure("source_download", "invalid_url", 0, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return downloadedFile{}, nil, photoFailure("source_download", "request", 0, err)
	}
	request.Header.Set("User-Agent", userAgent)
	if referer != "" {
		request.Header.Set("Referer", referer)
	}
	response, err := c.source.Do(request)
	if err != nil {
		return downloadedFile{}, nil, photoFailure("source_download", "request", 0, contextError(err))
	}
	defer c.closeResponseBody(response.Body)

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return downloadedFile{}, nil, photoFailure("source_download", "source_http", response.StatusCode, nil)
	}
	file, err := saveDownloadedFile(directory, response.Body)
	if err != nil {
		return downloadedFile{}, nil, photoFailure("source_download", "read", 0, err)
	}
	contentType := http.DetectContentType(file.content)
	file.content = nil
	file.contentType = normalizeContentType(contentType)
	responseURL := parsedURL
	if response.Request != nil && response.Request.URL != nil {
		responseURL = response.Request.URL
	}
	return file.downloadedFile, responseURL, nil
}

func (c *Client) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) > maxHTTPRedirects {
		return errors.New("photo HTTP redirect limit exceeded")
	}
	_, err := parseSourceURL(request.URL.String())

	return err
}

type savedFile struct {
	downloadedFile
	content []byte
}

func saveDownloadedFile(directory string, body io.Reader) (saved savedFile, returnErr error) {
	file, err := os.CreateTemp(directory, "photo-")
	if err != nil {
		return savedFile{}, fmt.Errorf("create temporary photo file: %w", err)
	}
	path := file.Name()
	closed := false
	success := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); closeErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close temporary photo: %w", closeErr))
			}
		}
		if !success {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary photo: %w", removeErr))
			}
		}
	}()

	limited := io.LimitReader(body, maxDownloadBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return savedFile{}, fmt.Errorf("read photo: %w", contextError(err))
	}
	if len(data) > maxDownloadBytes {
		return savedFile{}, fmt.Errorf("photo exceeds %d bytes", maxDownloadBytes)
	}
	if _, writeErr := file.Write(data); writeErr != nil {
		return savedFile{}, fmt.Errorf("save temporary photo: %w", writeErr)
	}
	closeErr := file.Close()
	closed = true
	if closeErr != nil {
		return savedFile{}, fmt.Errorf("close temporary photo: %w", closeErr)
	}
	success = true

	return savedFile{downloadedFile: downloadedFile{path: path}, content: data}, nil
}

func (c *Client) publishImage(ctx context.Context, path, contentType string) (photoResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	body, contentLength, requestContentType, err := multipartBody(path, contentType, c.tagID)
	if err != nil {
		return photoResult{}, photoFailure("slink_upload", "request_body", 0, err)
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "api/external/upload"})
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return photoResult{}, photoFailure("slink_upload", "request", 0, err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", requestContentType)
	request.Header.Set("Origin", c.baseURL.Scheme+"://"+c.baseURL.Host)
	request.Header.Set("User-Agent", userAgent)
	request.ContentLength = contentLength
	response, err := c.http.Do(request)
	if err != nil {
		return photoResult{}, photoFailure("slink_upload", "request", 0, contextError(err))
	}
	defer c.closeResponseBody(response.Body)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return photoResult{}, photoFailure("slink_upload", "slink_http", response.StatusCode, nil)
	}

	responseData, err := io.ReadAll(io.LimitReader(response.Body, maxUploadResponse+1))
	if err != nil {
		return photoResult{}, photoFailure("slink_upload", "response", 0, contextError(err))
	}
	if len(responseData) > maxUploadResponse {
		return photoResult{}, photoFailure("slink_upload", "response_too_large", 0, nil)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if decodeErr := json.Unmarshal(responseData, &payload); decodeErr != nil {
		return photoResult{}, photoFailure("slink_upload", "response", 0, decodeErr)
	}
	resolvedURL, err := c.resolveSlinkURL(payload.URL)
	if err != nil {
		return photoResult{}, photoFailure("slink_upload", "response_url", 0, err)
	}

	return photoResult{slinkURL: resolvedURL, slinkProfile: c.profile}, nil
}

func multipartBody(path, contentType, tagID string) (io.Reader, int64, string, error) {
	// The path is generated by os.CreateTemp inside the per-book temporary directory.
	file, err := os.Open(path) //nolint:gosec // path is an internal temporary file, not user-selected
	if err != nil {
		return nil, 0, "", fmt.Errorf("open temporary photo: %w", err)
	}
	data, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil {
		return nil, 0, "", fmt.Errorf("read temporary photo: %w", err)
	}
	if closeErr != nil {
		return nil, 0, "", fmt.Errorf("close temporary photo: %w", closeErr)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     "image",
		"filename": "image" + extensionForType(contentType),
	})
	filePart, err := multipartWriter.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {disposition},
		"Content-Type":        {contentType},
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("create Slink image field: %w", err)
	}
	if _, writeErr := filePart.Write(data); writeErr != nil {
		return nil, 0, "", fmt.Errorf("write Slink image field: %w", writeErr)
	}
	if writeErr := multipartWriter.WriteField("tagIds[]", tagID); writeErr != nil {
		return nil, 0, "", fmt.Errorf("write Slink tag field: %w", writeErr)
	}
	if multipartCloseErr := multipartWriter.Close(); multipartCloseErr != nil {
		return nil, 0, "", fmt.Errorf("close Slink multipart body: %w", multipartCloseErr)
	}

	return bytes.NewReader(body.Bytes()), int64(body.Len()), multipartWriter.FormDataContentType(), nil
}

func metaRefresh(path string, baseURL *url.URL) (string, bool, error) {
	// The path is generated by os.CreateTemp inside the per-book temporary directory.
	data, err := os.ReadFile(path) //nolint:gosec // path is an internal temporary file, not user-selected
	if err != nil {
		return "", false, fmt.Errorf("read HTML photo: %w", err)
	}
	document, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return "", false, fmt.Errorf("parse photo META refresh: %w", err)
	}
	for node := range document.Descendants() {
		if node.Type != html.ElementNode || node.Data != "meta" ||
			!strings.EqualFold(attribute(node, "http-equiv"), "refresh") {
			continue
		}
		content := attribute(node, "content")
		target, refreshErr := parseRefreshContent(content)
		if refreshErr != nil {
			return "", false, refreshErr
		}
		parsed, parseErr := url.Parse(target)
		if parseErr != nil {
			return "", false, fmt.Errorf("parse photo META refresh URL: %w", parseErr)
		}
		resolved := baseURL.ResolveReference(parsed)
		return resolved.String(), true, nil
	}

	return "", false, nil
}

func parseRefreshContent(content string) (string, error) {
	parts := strings.SplitN(content, ";", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid photo META refresh content")
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err != nil {
		return "", fmt.Errorf("invalid photo META refresh delay: %w", err)
	}
	rest := strings.TrimSpace(parts[1])
	if len(rest) < 4 || !strings.EqualFold(rest[:3], "url") || strings.TrimSpace(rest[3:]) == "" {
		return "", errors.New("invalid photo META refresh URL")
	}
	target := strings.TrimSpace(rest[3:])
	if target[0] != '=' {
		return "", errors.New("invalid photo META refresh URL")
	}
	target = strings.TrimSpace(target[1:])
	quoted := len(target) >= 2 && target[0] == '\'' && target[len(target)-1] == '\''
	quoted = quoted || len(target) >= 2 && target[0] == '"' && target[len(target)-1] == '"'
	if quoted {
		target = target[1 : len(target)-1]
	}
	if target == "" {
		return "", errors.New("invalid photo META refresh URL")
	}

	return target, nil
}

func (c *Client) resolveSlinkURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("slink response URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse slink image URL: %w", err)
	}
	resolved := c.baseURL.ResolveReference(parsed)
	if validationErr := validateHTTPURL(resolved); validationErr != nil {
		return "", fmt.Errorf("invalid slink image URL: %w", validationErr)
	}

	return resolved.String(), nil
}

func secureDialContext(
	lookupIP func(context.Context, string) ([]net.IP, error),
	dialContext func(context.Context, string, string) (net.Conn, error),
	timeout time.Duration,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse photo address: %w", err)
		}
		ips := []net.IP{net.ParseIP(host)}
		if ips[0] == nil {
			ips, err = lookupIP(dialCtx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve photo host: %w", err)
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("photo host has no addresses")
		}
		for _, ip := range ips {
			if ip == nil || len(iana.GetReservationsForIP(ip)) > 0 {
				return nil, errors.New("photo URL points to a restricted address")
			}
		}

		var dialErrors []error
		for _, ip := range ips {
			connection, dialErr := dialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			dialErrors = append(dialErrors, dialErr)
		}

		return nil, fmt.Errorf("dial photo host: %w", errors.Join(dialErrors...))
	}
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if validationErr := validateHTTPURL(parsed); validationErr != nil {
		return nil, validationErr
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("slink base URL must not contain userinfo, query, or fragment")
	}
	if parsed.Path != "" && !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}

	return parsed, nil
}

func parseSourceURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse photo URL: %w", err)
	}
	if parsed.User != nil {
		return nil, errors.New("photo URL userinfo is not supported")
	}
	if validationErr := validateHTTPURL(parsed); validationErr != nil {
		return nil, validationErr
	}

	return parsed, nil
}

func validateHTTPURL(parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("URL must use HTTP or HTTPS")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("URL host is required")
	}
	if parsed.User != nil {
		return errors.New("URL userinfo is not supported")
	}

	return nil
}

func (c *Client) reusableResult(photo alib.Photo) (photoResult, bool) {
	if photo.SlinkProfile != c.profile {
		return photoResult{}, false
	}
	if photo.SlinkURL == "" && !photo.NonImage {
		return photoResult{}, false
	}

	return photoResultFromPhoto(photo), true
}

func photoResultFromPhoto(photo alib.Photo) photoResult {
	return photoResult{slinkURL: photo.SlinkURL, slinkProfile: photo.SlinkProfile, nonImage: photo.NonImage}
}

func applyPhotoResult(photo *alib.Photo, result photoResult) {
	photo.SlinkURL = result.slinkURL
	photo.SlinkProfile = result.slinkProfile
	photo.NonImage = result.nonImage
}

func cloneBook(book alib.Book) alib.Book {
	book.Photos = append([]alib.Photo(nil), book.Photos...)
	return book
}

func (c *Client) logFailure(ctx context.Context, index int, err error) {
	failure := photoFailureDetailsFromError(err)
	attrs := []any{
		slog.Int(logKeyIndex, index),
		slog.String(logKeyError, "photo processing failed"),
		slog.String(logKeyErrorType, fmt.Sprintf("%T", err)),
		slog.String(logKeyStage, failure.stage),
		slog.String(logKeyErrorCat, failure.category),
	}
	if failure.status != 0 {
		attrs = append(attrs, slog.Int(logKeyHTTPStatus, failure.status))
	}
	c.logger.ErrorContext(ctx, "slink.photo_failed", attrs...)
}

type photoFailureDetails struct {
	stage    string
	category string
	status   int
}

type photoFailureError struct {
	err error
	photoFailureDetails
}

func (e *photoFailureError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("%s failed: %s (HTTP %d)", e.stage, e.category, e.status)
	}

	return fmt.Sprintf("%s failed: %s", e.stage, e.category)
}

func (e *photoFailureError) Unwrap() error {
	return e.err
}

func photoFailure(stage, category string, status int, err error) error {
	return &photoFailureError{
		photoFailureDetails: photoFailureDetails{stage: stage, category: category, status: status},
		err:                 err,
	}
}

func photoFailureDetailsFromError(err error) photoFailureDetails {
	var failure *photoFailureError
	if errors.As(err, &failure) {
		return failure.photoFailureDetails
	}

	return photoFailureDetails{stage: "processing", category: "unknown"}
}

func safeReferer(rawURL string) string {
	parsed, err := parseSourceURL(rawURL)
	if err != nil {
		return ""
	}
	parsed.Fragment = ""

	return parsed.String()
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	return err
}

func defaultLookupIP(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}

	return ips, nil
}

func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}

	return ""
}

func normalizeContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}

	return strings.ToLower(mediaType)
}

func isImageType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.HasPrefix(mediaType, "image/")
}

func extensionForType(contentType string) string {
	contentType = normalizeContentType(contentType)
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		extensions, err := mime.ExtensionsByType(contentType)
		if err == nil && len(extensions) > 0 {
			return extensions[0]
		}
		return "." + strings.TrimPrefix(contentType, "image/")
	}
}

func (c *Client) closeResponseBody(body io.Closer) {
	if err := body.Close(); err != nil {
		c.logger.Warn("slink.response_close_failed", slog.String(logKeyError, "response body close failed"))
	}
}
