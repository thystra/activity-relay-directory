package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var errStrictJSONObject = errors.New("strict JSON object is invalid")

func validOperationTarget(request *http.Request, endpointPath string) bool {
	return request != nil && request.URL != nil &&
		request.Method == http.MethodPost &&
		request.URL.EscapedPath() == endpointPath &&
		request.URL.RawQuery == "" && !request.URL.ForceQuery &&
		request.URL.Fragment == "" && request.URL.RawFragment == "" &&
		(request.RequestURI == "" || request.RequestURI == endpointPath)
}

func decodeStrictJSONObject(body []byte, destination any) error {
	if err := rejectDuplicateTopLevelJSONNames(body); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errStrictJSONObject
	}
	return nil
}

func rejectDuplicateTopLevelJSONNames(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errStrictJSONObject
	}

	seen := make(map[string]struct{}, 4)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errStrictJSONObject
		}
		name, ok := token.(string)
		if !ok {
			return errStrictJSONObject
		}
		if _, duplicate := seen[name]; duplicate {
			return errStrictJSONObject
		}
		seen[name] = struct{}{}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errStrictJSONObject
		}
	}

	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errStrictJSONObject
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errStrictJSONObject
	}
	return nil
}
