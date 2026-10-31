package micloud

import (
	"encoding/json"
	"strconv"
	"strings"
)

// flexString decodes a JSON string, number or null into a string.
// The service returns IDs sometimes as numbers (root parentId 0, userId) and
// sometimes as strings; numbers are kept verbatim so large IDs never lose
// precision through float64.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexString(v)
		return nil
	}
	*s = flexString(b)
	return nil
}

// flexInt decodes a JSON number, a numeric string or null into an int64.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	str := strings.Trim(string(b), `"`)
	if str == "" || str == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(str, 64)
		if ferr != nil {
			return err
		}
		v = int64(f)
	}
	*n = flexInt(v)
	return nil
}

// flexBool decodes true/false, 0/1 and their string forms.
type flexBool bool

func (v *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true", "1":
		*v = true
	default:
		*v = false
	}
	return nil
}
