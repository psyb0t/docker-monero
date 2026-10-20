package jsonrpc

import (
	"crypto/md5" //nolint:gosec // monerod's digest protocol requires MD5
	"crypto/rand"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

const (
	digestScheme         = "Digest"
	digestAlgorithm      = "MD5"
	digestQOP            = "auth"
	digestNonceCount     = "00000001"
	digestParamRealm     = "realm"
	digestParamNonce     = "nonce"
	digestParamQOP       = "qop"
	digestParamAlgorithm = "algorithm"
)

// Each challenge gets a fresh cnonce, so nonce counts need no shared state.
// Only a 401 is replayed. An accepted write is never sent a second time.
func (c *Client) do(request *http.Request) (*http.Response, error) {
	client := c.httpClient
	if c.login != "" {
		// epee binds its digest nonce to the TCP connection. A private pool
		// keeps concurrent RPCs from taking the challenged connection.
		transport := &http.Transport{}
		defer transport.CloseIdleConnections()

		copyClient := *c.httpClient
		copyClient.Transport = transport
		client = &copyClient
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, classifyTransportError(request.Context(), err)
	}

	if response.StatusCode != http.StatusUnauthorized || c.login == "" {
		return response, nil
	}

	challenge := response.Header.Get(aichteeteapee.HeaderNameWWWAuthenticate)
	// Drain the challenge so the retry can use the same TCP connection.
	read, readErr := io.Copy(io.Discard,
		io.LimitReader(response.Body, c.maxResponseBytes+1))
	_ = response.Body.Close()

	if readErr != nil || read > c.maxResponseBytes {
		return nil, ctxerrors.Wrap(
			commerr.ErrReadFailed, "read RPC authentication challenge",
		)
	}

	authorization, err := digestAuthorization(request, challenge, c.login)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "authenticate daemon RPC")
	}

	retry := request.Clone(request.Context())

	retry.Body, err = request.GetBody()
	if err != nil {
		return nil, ctxerrors.Wrap(commerr.ErrReadFailed, "replay RPC body")
	}

	retry.Header.Set(aichteeteapee.HeaderNameAuthorization, authorization)

	response, err = client.Do(retry)
	if err != nil {
		return nil, classifyTransportError(request.Context(), err)
	}

	return response, nil
}

func digestAuthorization(
	request *http.Request,
	header, login string,
) (string, error) {
	parameters, err := digestParameters(header)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse digest challenge")
	}

	algorithm := parameters[digestParamAlgorithm]
	if (algorithm != "" && !strings.EqualFold(algorithm, digestAlgorithm)) ||
		!hasAuthQOP(parameters[digestParamQOP]) ||
		parameters[digestParamNonce] == "" {
		return "", ctxerrors.Wrap(
			commerr.ErrNotAuthenticated, "unsupported digest challenge",
		)
	}

	username, password, ok := strings.Cut(login, ":")
	if !ok || username == "" ||
		strings.ContainsFunc(username, unicode.IsControl) {
		return "", ctxerrors.Wrap(
			commerr.ErrNotAuthenticated, "invalid RPC login format",
		)
	}

	cnonce := rand.Text()
	uri := request.URL.RequestURI()
	ha1 := digestHash(strings.Join([]string{
		username, parameters[digestParamRealm], password,
	}, ":"))
	ha2 := digestHash(request.Method + ":" + uri)
	response := digestHash(strings.Join([]string{
		ha1, parameters[digestParamNonce], digestNonceCount,
		cnonce, digestQOP, ha2,
	}, ":"))

	fields := []string{
		"username=" + digestQuote(username),
		"realm=" + digestQuote(parameters[digestParamRealm]),
		"nonce=" + digestQuote(parameters[digestParamNonce]),
		"uri=" + digestQuote(uri),
		"response=" + digestQuote(response),
		"algorithm=" + digestAlgorithm,
		"qop=" + digestQOP,
		"nc=" + digestNonceCount,
		"cnonce=" + digestQuote(cnonce),
	}
	if opaque, exists := parameters["opaque"]; exists {
		fields = append(fields, "opaque="+digestQuote(opaque))
	}

	return digestScheme + " " + strings.Join(fields, ", "), nil
}

// MIME parameters use the same token and quoted-string syntax, with semicolons
// in place of commas. Preserve commas inside quoted realms and qop lists.
func digestParameters(header string) (map[string]string, error) {
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, digestScheme) {
		return nil, ctxerrors.Wrap(
			commerr.ErrNotAuthenticated, "missing digest challenge",
		)
	}

	_, parameters, err := mime.ParseMediaType(
		"Digest;" + digestMIMEParameters(rest),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(
			commerr.ErrNotAuthenticated, "malformed digest challenge",
		)
	}

	return parameters, nil
}

func digestMIMEParameters(value string) string {
	var converted strings.Builder

	quoted, escaped := false, false

	for _, character := range value {
		switch {
		case escaped:
			escaped = false
		case quoted && character == '\\':
			escaped = true
		case character == '"':
			quoted = !quoted
		case !quoted && character == ',':
			character = ';'
		}

		converted.WriteRune(character)
	}

	return converted.String()
}

func hasAuthQOP(value string) bool {
	for option := range strings.SplitSeq(value, ",") {
		if strings.TrimSpace(option) == digestQOP {
			return true
		}
	}

	return false
}

func digestQuote(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)

	return `"` + escaped + `"`
}

//nolint:gosec // monerod's digest protocol requires MD5
func digestHash(value string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(value)))
}
