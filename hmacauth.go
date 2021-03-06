package hmacauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	// common parameters
	authorizationHeader = "Authorization"
	apiKeyParam         = "APIKey"
	signatureParam      = "Signature"
	timestampParam      = "Timestamp"

	// timestamp validation
	maxNegativeTimeOffset time.Duration = -20 * time.Second

	// parsing bits
	empty   = ""
	comma   = ","
	space   = " "
	eqSign  = "="
	newline = "\n"
)

type (
	middleware func(http.ResponseWriter, *http.Request)
	KeyLocator func(string) string
)

type Options struct {
	SignedHeaders      []string
	SecretKey          KeyLocator
	SignatureExpiresIn time.Duration
}

type authBits struct {
	APIKey          string
	Signature       string
	TimestampString string
	Timestamp       time.Time
}

func (ab *authBits) IsValid() bool {
	return ab.APIKey != empty &&
		ab.Signature != empty &&
		!ab.Timestamp.IsZero()
}

func (ab *authBits) SetTimestamp(isoTime string) (err error) {
	ab.Timestamp, err = time.Parse(time.RFC3339, isoTime)
	if err == nil {
		ab.TimestampString = isoTime
	}
	return
}

type HMACAuth struct {
	options Options
}

func (ha HMACAuth) ServeHTTP(res http.ResponseWriter, req *http.Request, next http.HandlerFunc) {
	// Validate options
	if ha.options.SecretKey == nil {
		panic(secretKeyRequired)
	}

	var (
		err error
		ab  *authBits
	)

	if ab, err = parseAuthHeader(req.Header.Get(authorizationHeader)); err == nil {
		if err = validateTimestamp(ab.Timestamp, &ha.options); err == nil {
			var sts string
			if sts, err = StringToSign(req, &ha.options, ab.TimestampString); err == nil {
				if sk := ha.options.SecretKey(ab.APIKey); sk != empty {
					if ab.Signature != SignString(sts, sk) {
						err = HMACAuthError{invalidSignature}
					}
				} else {
					err = HMACAuthError{invalidAPIKey}
				}
			}
		}
	}

	if err != nil {
		log.Println(err.Error())
		http.Error(res, err.Error(), 401)
		return
	} else {
		next(res, req)
	}
}

func New(options Options) *HMACAuth {
	return &HMACAuth{options}
}

func SignString(str string, secret string) string {
	hash := hmac.New(sha256.New, []byte(secret))
	hash.Write([]byte(str))
	return base64.StdEncoding.EncodeToString(hash.Sum(nil))
}

func StringToSign(req *http.Request, options *Options, timestamp string) (string, error) {
	var buffer bytes.Buffer

	// Standard
	buffer.WriteString(req.Method)
	buffer.WriteString(newline)
	buffer.WriteString(req.Host)
	buffer.WriteString(newline)
	//buffer.WriteString(req.URL.RequestURI())
	buffer.WriteString(fmt.Sprintf("%s?%s", req.URL.Path, req.URL.RawQuery))
	buffer.WriteString(newline)
	buffer.WriteString(timestamp)
	buffer.WriteString(newline)

	// Headers
	sort.Strings(options.SignedHeaders)
	for _, header := range options.SignedHeaders {
		val := req.Header.Get(header)
		if val == empty {
			return empty, HeaderMissingError{header}
		}
		buffer.WriteString(val)
		buffer.WriteString(newline)
	}

	return buffer.String(), nil
}

func parseAuthHeader(header string) (*authBits, error) {
	if header == empty {
		return nil, HeaderMissingError{authorizationHeader}
	}

	ab := new(authBits)
	parts := strings.Split(header, comma)
	for _, part := range parts {
		kv := strings.SplitN(strings.Trim(part, space), eqSign, 2)
		if kv[0] == apiKeyParam {
			if ab.APIKey != empty {
				return nil, RepeatedParameterError{kv[0]}
			}
			ab.APIKey = kv[1]
		} else if kv[0] == signatureParam {
			if ab.Signature != empty {
				return nil, RepeatedParameterError{kv[0]}
			}
			ab.Signature = kv[1]
		} else if kv[0] == timestampParam {
			if !ab.Timestamp.IsZero() {
				return nil, RepeatedParameterError{kv[0]}
			}
			if ab.SetTimestamp(kv[1]) != nil {
				return nil, HMACAuthError{invalidTimestamp}
			}
		} else {
			return nil, HMACAuthError{invalidParameter}
		}
	}

	if !ab.IsValid() {
		return nil, HMACAuthError{missingParameter}
	}

	return ab, nil
}

func validateTimestamp(ts time.Time, options *Options) error {
	reqAge := time.Since(ts)

	// Allow for about `maxNegativeTimeOffset` of difference, some servers are
	// ahead and some are behind
	if reqAge < maxNegativeTimeOffset {
		return HMACAuthError{tsOutOfRange}
	}

	if options.SignatureExpiresIn != 0 {
		if reqAge > options.SignatureExpiresIn {
			return HMACAuthError{signatureExpired}
		}
	}

	return nil
}
