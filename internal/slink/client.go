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
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/kemko/alib-fetcher/internal/alib"
)

const (
	maxDownloadBytes  = 15 << 20
	maxUploadResponse = 1 << 20
	maxMetaRedirects  = 5
	maxHTTPRedirects  = 5
	logKeyError       = "error"
	logKeyErrorType   = "error_type"
	logKeyIndex       = "index"
)

// Options configures the HTTP and DNS dependencies of Client.
type Options struct {
	HTTPClient *http.Client
	LookupIP   func(context.Context, string) ([]net.IP, error)
}

// PreparedBook contains a book with successful Slink processing results.
//
//nolint:govet // fieldalignment: book payload and cleanup closures form one lifecycle result.
type PreparedBook struct {
	Book               alib.Book
	cleanup            func() error
	temporaryDirectory func() string
}

// Cleanup removes the temporary files for the book. It is safe to call more than once.
func (p *PreparedBook) Cleanup() error {
	if p == nil || p.cleanup == nil {
		return nil
	}

	return p.cleanup()
}

// TemporaryDirectory returns the book's temporary directory for lifecycle coordination.
func (p *PreparedBook) TemporaryDirectory() string {
	if p == nil || p.temporaryDirectory == nil {
		return ""
	}

	return p.temporaryDirectory()
}

// Client downloads photo files and uploads images to Slink.
type Client struct {
	baseURL  *url.URL
	http     *http.Client
	lookupIP func(context.Context, string) ([]net.IP, error)
	logger   *slog.Logger
	apiKey   string
	tagID    string
	profile  string
	timeout  time.Duration
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

	profileHash := sha256.Sum256([]byte(baseURL.String() + "\x00" + tagID))
	client := &Client{
		baseURL:  baseURL,
		apiKey:   apiKey,
		tagID:    tagID,
		profile:  "slink:" + hex.EncodeToString(profileHash[:8]),
		timeout:  timeout,
		http:     httpClient,
		lookupIP: lookupIP,
		logger:   logger,
	}
	client.http.CheckRedirect = client.checkRedirect

	return client, nil
}

// Profile returns the stable identifier for this Slink configuration.
func (c *Client) Profile() string {
	return c.profile
}

// Prepare downloads and publishes a book's photos in source order.
func (c *Client) Prepare(ctx context.Context, book alib.Book) (*PreparedBook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prepared := &PreparedBook{Book: cloneBook(book)}
	if len(prepared.Book.Photos) == 0 {
		return prepared, nil
	}

	directory, err := os.MkdirTemp("", "alib-fetcher-photos-")
	if err != nil {
		return nil, fmt.Errorf("create temporary photo directory: %w", err)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	prepared.temporaryDirectory = func() string { return directory }
	prepared.cleanup = func() error {
		cleanupOnce.Do(func() {
			cleanupErr = os.RemoveAll(directory)
		})
		return cleanupErr
	}
	if prepareErr := c.preparePhotos(ctx, prepared); prepareErr != nil {
		return nil, errors.Join(prepareErr, prepared.Cleanup())
	}

	return prepared, nil
}

// Process is an alias for Prepare for photo processor callers.
func (c *Client) Process(ctx context.Context, book alib.Book) (*PreparedBook, error) {
	return c.Prepare(ctx, book)
}

func (c *Client) preparePhotos(ctx context.Context, prepared *PreparedBook) error {
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

		result, file, processErr := c.processPhoto(ctx, prepared.TemporaryDirectory(), *photo)
		if processErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logFailure(ctx, index, processErr)
			continue
		}
		if file != "" {
			result, processErr = c.publishImage(ctx, file, result.contentType)
			if processErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.logFailure(ctx, index, processErr)
				continue
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

func (c *Client) processPhoto(ctx context.Context, directory string, photo alib.Photo) (photoResult, string, error) {
	currentURL := photo.URL
	visited := make(map[string]struct{})
	for metaRedirect := 0; ; metaRedirect++ {
		if err := ctx.Err(); err != nil {
			return photoResult{}, "", err
		}
		if _, found := visited[currentURL]; found {
			return photoResult{}, "", errors.New("photo META refresh cycle")
		}
		visited[currentURL] = struct{}{}

		file, responseURL, err := c.download(ctx, directory, currentURL)
		if err != nil {
			return photoResult{}, "", err
		}
		nextURL, found, parseErr := metaRefresh(file.path, responseURL)
		if parseErr != nil {
			return photoResult{}, "", parseErr
		}
		if !found {
			if !isImageType(file.contentType) {
				return photoResult{nonImage: true, slinkProfile: c.profile}, "", nil
			}
			return photoResult{contentType: file.contentType}, file.path, nil
		}
		if metaRedirect >= maxMetaRedirects {
			return photoResult{}, "", errors.New("photo META refresh limit exceeded")
		}
		currentURL = nextURL
	}
}

func (c *Client) download(ctx context.Context, directory, rawURL string) (downloadedFile, *url.URL, error) {
	parsedURL, err := parseSourceURL(rawURL)
	if err != nil {
		return downloadedFile{}, nil, err
	}
	if validationErr := c.validateSourceURL(ctx, parsedURL); validationErr != nil {
		return downloadedFile{}, nil, validationErr
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return downloadedFile{}, nil, fmt.Errorf("create photo request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return downloadedFile{}, nil, contextError(err)
	}
	defer c.closeResponseBody(response.Body)

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return downloadedFile{}, nil, fmt.Errorf("photo download returned status %d", response.StatusCode)
	}
	file, err := saveDownloadedFile(directory, response.Body)
	if err != nil {
		return downloadedFile{}, nil, err
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
	if len(via) >= maxHTTPRedirects {
		return errors.New("photo HTTP redirect limit exceeded")
	}
	return c.validateSourceURL(request.Context(), request.URL)
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
	success := false
	defer func() {
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
	if closeErr := file.Close(); closeErr != nil {
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
		return photoResult{}, err
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "/api/external/upload"})
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return photoResult{}, fmt.Errorf("create Slink upload request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", requestContentType)
	request.ContentLength = contentLength
	response, err := c.http.Do(request)
	if err != nil {
		return photoResult{}, contextError(err)
	}
	defer c.closeResponseBody(response.Body)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return photoResult{}, fmt.Errorf("slink upload returned status %d", response.StatusCode)
	}

	responseData, err := io.ReadAll(io.LimitReader(response.Body, maxUploadResponse+1))
	if err != nil {
		return photoResult{}, fmt.Errorf("read Slink response: %w", contextError(err))
	}
	if len(responseData) > maxUploadResponse {
		return photoResult{}, fmt.Errorf("slink response exceeds %d bytes", maxUploadResponse)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if decodeErr := json.Unmarshal(responseData, &payload); decodeErr != nil {
		return photoResult{}, fmt.Errorf("decode slink response: %w", decodeErr)
	}
	resolvedURL, err := c.resolveSlinkURL(payload.URL)
	if err != nil {
		return photoResult{}, err
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
	filePart, err := multipartWriter.CreateFormFile("image", "image"+extensionForType(contentType))
	if err != nil {
		return nil, 0, "", fmt.Errorf("create Slink image field: %w", err)
	}
	if _, writeErr := filePart.Write(data); writeErr != nil {
		return nil, 0, "", fmt.Errorf("write Slink image field: %w", writeErr)
	}
	if writeErr := multipartWriter.WriteField("tagIds", tagID); writeErr != nil {
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

func (c *Client) validateSourceURL(ctx context.Context, parsedURL *url.URL) error {
	if validationErr := validateHTTPURL(parsedURL); validationErr != nil {
		return validationErr
	}
	host := parsedURL.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if restrictedIP(ip) {
			return errors.New("photo URL points to a restricted address")
		}
		return nil
	}
	ips, err := c.lookupIP(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve photo host: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("photo host has no addresses")
	}
	for _, ip := range ips {
		if restrictedIP(ip) {
			return errors.New("photo URL points to a restricted address")
		}
	}

	return nil
}

func restrictedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if validationErr := validateHTTPURL(parsed); validationErr != nil {
		return nil, validationErr
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
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
	c.logger.ErrorContext(ctx, "slink.photo_failed",
		slog.Int(logKeyIndex, index),
		slog.String(logKeyError, "photo processing failed"),
		slog.String(logKeyErrorType, fmt.Sprintf("%T", err)),
	)
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
