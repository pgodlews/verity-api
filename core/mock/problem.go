package mock

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxBody = 1 << 20

var errBodyTooLarge = errors.New("body exceeds 1MiB")

func readLimited(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, errBodyTooLarge
	}
	return b, nil
}

type problem struct {
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Status int      `json:"status"`
	Detail string   `json:"detail,omitempty"`
	Errors []string `json:"errors,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, title, detail string, errs []string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
		Errors: errs,
	})
}
