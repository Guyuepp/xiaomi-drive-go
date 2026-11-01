package xiaomidrive

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

func downloadRange(offset, length int64) (string, error) {
	if offset < 0 || (length > 0 && offset > math.MaxInt64-(length-1)) {
		return "", fmt.Errorf("%w: invalid download range", ErrInvalidArgument)
	}
	if length > 0 {
		return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1), nil
	}
	if offset > 0 {
		return fmt.Sprintf("bytes=%d-", offset), nil
	}
	return "", nil
}

type downloadBody struct {
	reader io.LimitedReader
	io.Closer
	exact bool
}

func (r *downloadBody) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF && r.exact && r.reader.N > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// downloadResponse refuses ignored or misplaced ranges, and never exposes
// bytes beyond either the requested length or the declared partial response.
func downloadResponse(resp *http.Response, offset, length int64, label string) (io.ReadCloser, error) {
	fail := func(reason string) (io.ReadCloser, error) {
		resp.Body.Close()
		return nil, fmt.Errorf("xiaomidrive: %s: HTTP %d: %s", label, resp.StatusCode, reason)
	}
	limit := length
	exact := false
	switch resp.StatusCode {
	case http.StatusOK:
		if offset != 0 {
			return fail("server ignored requested offset")
		}
		if resp.ContentLength >= 0 {
			if limit < 0 || resp.ContentLength < limit {
				limit = resp.ContentLength
			}
			exact = true
		}
	case http.StatusPartialContent:
		value, ok := strings.CutPrefix(resp.Header.Get("Content-Range"), "bytes ")
		bounds, total, slash := strings.Cut(value, "/")
		startText, endText, dash := strings.Cut(bounds, "-")
		start, e1 := strconv.ParseInt(startText, 10, 64)
		end, e2 := strconv.ParseInt(endText, 10, 64)
		if !ok || !slash || !dash || e1 != nil || e2 != nil || start != offset || end < start || end-start == math.MaxInt64 {
			return fail("invalid Content-Range")
		}
		if total != "*" {
			size, err := strconv.ParseInt(total, 10, 64)
			if err != nil || size <= end {
				return fail("invalid Content-Range total")
			}
			expectedEnd := size - 1
			if length > 0 && offset+length-1 < expectedEnd {
				expectedEnd = offset + length - 1
			}
			if end != expectedEnd {
				return fail("Content-Range is incomplete")
			}
		}
		span := end - start + 1
		if length >= 0 && span > length {
			return fail("Content-Range exceeds requested length")
		}
		if resp.ContentLength >= 0 && resp.ContentLength != span {
			return fail("Content-Length disagrees with Content-Range")
		}
		limit = span
		exact = true
	default:
		return fail("download request failed")
	}
	if limit >= 0 {
		return &downloadBody{
			reader: io.LimitedReader{R: resp.Body, N: limit},
			Closer: resp.Body,
			exact:  exact,
		}, nil
	}
	return resp.Body, nil
}
