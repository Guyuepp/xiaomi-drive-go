package micloud

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. Use errors.Is to test for them; the concrete error is
// usually an *APIError, *TaskError or *LoginError carrying the raw code.
var (
	// ErrUnauthorized means the session is gone and could not be restored
	// silently. The user has to log in again.
	ErrUnauthorized = errors.New("micloud: not logged in or session expired")
	// ErrNotFound means the file, folder or trash item does not exist (11002).
	ErrNotFound = errors.New("micloud: not found")
	// ErrExist means another item already uses the name (11005).
	ErrExist = errors.New("micloud: name already exists")
	// ErrInvalidName means the name breaks the service's naming rules (10002).
	ErrInvalidName = errors.New("micloud: invalid name")
	// ErrInvalidArgument means a request parameter was rejected (10008, 10016, -1).
	ErrInvalidArgument = errors.New("micloud: invalid argument")
	// ErrEmptyFile is returned for 0-byte uploads; the service cannot store them.
	ErrEmptyFile = errors.New("micloud: empty files cannot be uploaded")
	// ErrIntoSelf means a folder was moved or copied into itself (11010).
	ErrIntoSelf = errors.New("micloud: cannot move or copy a folder into itself")
	// ErrSameFolder means an item was copied into its own parent (11003).
	ErrSameFolder = errors.New("micloud: cannot copy into the same folder")
	// ErrNotDir is returned when a folder was expected.
	ErrNotDir = errors.New("micloud: not a folder")
	// ErrIsDir is returned when a file was expected.
	ErrIsDir = errors.New("micloud: is a folder")

	// ErrQRExpired means the QR code timed out before it was confirmed.
	ErrQRExpired = errors.New("micloud: QR code expired")
	// ErrCaptchaRequired means the account site asked for a captcha.
	ErrCaptchaRequired = errors.New("micloud: captcha required")
	// ErrVerificationRequired means the account site asked for an extra
	// verification step that must be completed in a browser.
	ErrVerificationRequired = errors.New("micloud: additional verification required")
)

// Service error codes observed on i.mi.com.
const (
	CodeUnknown         = -1
	CodeInvalidName     = 10002
	CodeInvalidArgument = 10008
	CodeRPC             = 10011
	CodeMissingArgument = 10016
	CodeNotFound        = 11002
	CodeCopySameFolder  = 11003
	CodeNameConflict    = 11005
	CodeIntoSelf        = 11010
	CodeStorage         = 50005
	CodeQRExpired       = 70031
)

func sentinelFor(code int) error {
	switch code {
	case 401:
		return ErrUnauthorized
	case CodeNotFound:
		return ErrNotFound
	case CodeNameConflict:
		return ErrExist
	case CodeInvalidName:
		return ErrInvalidName
	case CodeInvalidArgument, CodeMissingArgument, CodeUnknown, CodeRPC:
		return ErrInvalidArgument
	case CodeIntoSelf:
		return ErrIntoSelf
	case CodeCopySameFolder:
		return ErrSameFolder
	}
	return nil
}

// APIError is a failed i.mi.com request. The service's "retriable" flag is
// deliberately not exposed: it is set on errors that must not be retried.
type APIError struct {
	Endpoint    string
	HTTPStatus  int
	Code        int
	Reason      string
	Description string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "micloud: %s: ", e.Endpoint)
	if e.HTTPStatus == 401 {
		b.WriteString("unauthorized")
		return b.String()
	}
	if e.HTTPStatus != 0 && e.HTTPStatus != 200 {
		fmt.Fprintf(&b, "HTTP %d, ", e.HTTPStatus)
	}
	fmt.Fprintf(&b, "code %d", e.Code)
	if e.Reason != "" {
		fmt.Fprintf(&b, ": %s", e.Reason)
	} else if e.Description != "" {
		fmt.Fprintf(&b, ": %s", e.Description)
	}
	return b.String()
}

func (e *APIError) Is(target error) bool {
	if e.HTTPStatus == 401 {
		return target == ErrUnauthorized
	}
	return sentinelFor(e.Code) == target && target != nil
}

// FailedRecord is one entry of a task's failedRecords list.
type FailedRecord struct {
	ID      string
	Name    string
	Code    int
	Message string
}

// TaskError is a MOVE / COPY / DELETE task that failed as a whole or for
// some of its records.
type TaskError struct {
	Op      string
	TaskID  string
	Code    int
	Message string
	Failed  []FailedRecord
}

func codeText(code int) string {
	switch code {
	case CodeNameConflict:
		return "name already exists in target"
	case CodeIntoSelf:
		return "folder moved or copied into itself"
	case CodeCopySameFolder:
		return "copy into the same folder"
	case CodeNotFound:
		return "not found"
	}
	return ""
}

func (e *TaskError) Error() string {
	if len(e.Failed) > 0 && e.Code == 0 {
		f := e.Failed[0]
		who := f.Name
		if who == "" {
			who = f.ID
		}
		msg := f.Message
		if msg == "" {
			msg = codeText(f.Code)
		}
		return fmt.Sprintf("micloud: %s: %d item(s) failed, first %q: code %d %s", strings.ToLower(e.Op), len(e.Failed), who, f.Code, msg)
	}
	return fmt.Sprintf("micloud: %s: task failed: code %d %s", strings.ToLower(e.Op), e.Code, e.Message)
}

func (e *TaskError) Is(target error) bool {
	if target == nil {
		return false
	}
	if sentinelFor(e.Code) == target {
		return true
	}
	for _, f := range e.Failed {
		if sentinelFor(f.Code) == target {
			return true
		}
	}
	return false
}

// LoginError is a failed step of a login flow on account.xiaomi.com.
type LoginError struct {
	Step        string
	Code        int
	Description string
	// NotificationURL is a page where the user must finish an extra
	// verification step, if the account site asked for one.
	NotificationURL string
	// CaptchaURL is set when the account site demands a captcha.
	CaptchaURL string
}

func (e *LoginError) Error() string {
	msg := fmt.Sprintf("micloud: login (%s) failed: code %d", e.Step, e.Code)
	if e.Description != "" {
		msg += ": " + e.Description
	}
	switch {
	case e.CaptchaURL != "":
		msg += " (captcha required)"
	case e.NotificationURL != "":
		msg += " (additional verification required)"
	}
	return msg
}

func (e *LoginError) Is(target error) bool {
	switch target {
	case ErrQRExpired:
		return e.Code == CodeQRExpired
	case ErrCaptchaRequired:
		return e.CaptchaURL != ""
	case ErrVerificationRequired:
		return e.NotificationURL != ""
	case ErrUnauthorized:
		return e.Step == "restore"
	}
	return false
}
